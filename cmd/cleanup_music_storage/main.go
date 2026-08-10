package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/storage"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type objectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
}

type copyItem struct {
	Source string
	Target string
	Size   int64
}

type cleanupPlan struct {
	Copies             map[string]copyItem
	AlbumCovers        map[uuid.UUID]string
	SongAudio          map[uuid.UUID]string
	SongCovers         map[uuid.UUID]string
	ArtistImages       map[uuid.UUID]string
	PlaylistCovers     map[uuid.UUID]string
	RevisionSnapshots  map[uuid.UUID][]byte
	ProtectedKeys      map[string]bool
	DeleteKeys         map[string]objectInfo
	MissingReferences  map[string]bool
	ReferencedTempKeys map[string]bool
}

type planner struct {
	prefix  string
	objects map[string]objectInfo
	plan    *cleanupPlan
}

func main() {
	apply := flag.Bool("apply", false, "copy media, update database references, and delete unreferenced temporary objects")
	envFile := flag.String("env", ".env.prod", "environment file")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		log.Fatalf("load env: %v", err)
	}
	db, err := gorm.Open(postgres.Open(os.Getenv("DATABASE_URL")), &gorm.Config{})
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}
	client, err := storage.InitS3Client()
	if err != nil {
		log.Fatalf("init object storage: %v", err)
	}
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	prefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
	objects, err := listMusicTemporaryObjects(client, bucket)
	if err != nil {
		log.Fatalf("list temporary objects: %v", err)
	}
	plan, err := buildCleanupPlan(db, prefix, objects, time.Now().UTC())
	if err != nil {
		log.Fatalf("build cleanup plan: %v", err)
	}
	printPlan(plan)
	if !*apply {
		log.Println("dry run only; rerun with -apply after reviewing the plan")
		return
	}
	if len(plan.MissingReferences) > 0 {
		log.Fatalf("refusing cleanup: %d database media references point to missing objects", len(plan.MissingReferences))
	}
	if err := applyCopies(client, bucket, plan.Copies); err != nil {
		log.Fatalf("copy media: %v", err)
	}
	if err := applyDatabaseUpdates(db, plan); err != nil {
		log.Fatalf("update database references: %v", err)
	}
	remaining, err := collectTemporaryReferenceKeys(db, prefix)
	if err != nil {
		log.Fatalf("verify database references: %v", err)
	}
	for key := range remaining {
		delete(plan.DeleteKeys, key)
	}
	if err := deleteObjects(client, bucket, plan.DeleteKeys); err != nil {
		log.Fatalf("delete temporary objects: %v", err)
	}
	log.Printf("cleanup complete: copied=%d deleted=%d retained_references=%d", len(plan.Copies), len(plan.DeleteKeys), len(remaining))
}

func listMusicTemporaryObjects(client *s3.S3, bucket string) (map[string]objectInfo, error) {
	objects := map[string]objectInfo{}
	for _, prefix := range []string{"music/album-imports/", "music/covers/", "music/audio/uploads/"} {
		input := &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix)}
		for {
			output, err := client.ListObjectsV2(input)
			if err != nil {
				return nil, err
			}
			for _, item := range output.Contents {
				key := aws.StringValue(item.Key)
				objects[key] = objectInfo{Key: key, Size: aws.Int64Value(item.Size), LastModified: aws.TimeValue(item.LastModified)}
			}
			if !aws.BoolValue(output.IsTruncated) {
				break
			}
			input.ContinuationToken = output.NextContinuationToken
		}
	}
	return objects, nil
}

func newCleanupPlan() *cleanupPlan {
	return &cleanupPlan{
		Copies: map[string]copyItem{}, AlbumCovers: map[uuid.UUID]string{}, SongAudio: map[uuid.UUID]string{},
		SongCovers: map[uuid.UUID]string{}, ArtistImages: map[uuid.UUID]string{}, PlaylistCovers: map[uuid.UUID]string{},
		RevisionSnapshots: map[uuid.UUID][]byte{}, ProtectedKeys: map[string]bool{}, DeleteKeys: map[string]objectInfo{},
		MissingReferences: map[string]bool{}, ReferencedTempKeys: map[string]bool{},
	}
}

func buildCleanupPlan(db *gorm.DB, prefix string, objects map[string]objectInfo, now time.Time) (*cleanupPlan, error) {
	plan := newCleanupPlan()
	p := &planner{prefix: prefix, objects: objects, plan: plan}

	var albums []model.Album
	if err := db.Find(&albums).Error; err != nil {
		return nil, err
	}
	for _, album := range albums {
		if next := p.relocate(album.CoverURL, storage.BuildMusicAlbumCoverVersionKey(album.ID.String(), migratedAssetID(album.CoverURL, album.ID.String()), path.Ext(album.CoverURL))); next != album.CoverURL {
			plan.AlbumCovers[album.ID] = next
		}
	}

	var songs []model.Song
	if err := db.Find(&songs).Error; err != nil {
		return nil, err
	}
	songsByID := make(map[uuid.UUID]model.Song, len(songs))
	for _, song := range songs {
		songsByID[song.ID] = song
		if next := p.relocate(song.AudioURL, songAudioKey(song, song.AudioURL)); next != song.AudioURL {
			plan.SongAudio[song.ID] = next
		}
		if next := p.relocate(song.CoverURL, songCoverKey(song, song.CoverURL)); next != song.CoverURL {
			plan.SongCovers[song.ID] = next
		}
	}

	var artists []model.Artist
	if err := db.Find(&artists).Error; err != nil {
		return nil, err
	}
	for _, artist := range artists {
		key := storage.BuildMusicArtistImageVersionKey(artist.ID.String(), migratedAssetID(artist.ImageURL, artist.ID.String()), path.Ext(artist.ImageURL))
		if next := p.relocate(artist.ImageURL, key); next != artist.ImageURL {
			plan.ArtistImages[artist.ID] = next
		}
	}

	var playlists []model.Playlist
	if err := db.Find(&playlists).Error; err != nil {
		return nil, err
	}
	for _, playlist := range playlists {
		key := storage.BuildMusicPlaylistCoverVersionKey(playlist.ID.String(), migratedAssetID(playlist.CoverURL, playlist.ID.String()), path.Ext(playlist.CoverURL))
		if next := p.relocate(playlist.CoverURL, key); next != playlist.CoverURL {
			plan.PlaylistCovers[playlist.ID] = next
		}
	}

	var revisions []model.Revision
	if err := db.Find(&revisions).Error; err != nil {
		return nil, err
	}
	for _, revision := range revisions {
		next, changed, err := rewriteRevisionSnapshot(p, revision, songsByID)
		if err != nil {
			return nil, fmt.Errorf("revision %s: %w", revision.ID, err)
		}
		if changed {
			plan.RevisionSnapshots[revision.ID] = next
		}
	}

	if err := planTemporaryDeletes(db, plan, objects, now); err != nil {
		return nil, err
	}
	for key := range plan.ProtectedKeys {
		delete(plan.DeleteKeys, key)
	}
	return plan, nil
}

func (p *planner) relocate(rawURL, destinationKey string) string {
	key, ok := objectKey(rawURL, p.prefix)
	if !ok || !isTemporaryMusicKey(key) {
		return rawURL
	}
	p.plan.ReferencedTempKeys[key] = true
	item, exists := p.objects[key]
	if !exists {
		p.plan.MissingReferences[key] = true
		p.plan.ProtectedKeys[key] = true
		return rawURL
	}
	copyID := key + "\x00" + destinationKey
	p.plan.Copies[copyID] = copyItem{Source: key, Target: destinationKey, Size: item.Size}
	return p.prefix + "/" + destinationKey
}

func objectKey(rawURL, prefix string) (string, bool) {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "music/") {
		return strings.TrimLeft(rawURL, "/"), true
	}
	if rawURL == "" || prefix == "" || !strings.HasPrefix(rawURL, prefix+"/") {
		return "", false
	}
	key, err := url.PathUnescape(strings.TrimPrefix(rawURL, prefix+"/"))
	return strings.TrimLeft(key, "/"), err == nil && key != ""
}

func isTemporaryMusicKey(key string) bool {
	return strings.HasPrefix(key, "music/album-imports/") || strings.HasPrefix(key, "music/covers/") || strings.HasPrefix(key, "music/audio/uploads/")
}

func migratedAssetID(rawURL, owner string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(rawURL) + "\x00" + owner))
	return "migrated-" + hex.EncodeToString(sum[:10])
}

func songAudioKey(song model.Song, rawURL string) string {
	assetID := migratedAssetID(rawURL, song.ID.String())
	if song.AlbumID != nil {
		return storage.BuildMusicAlbumTrackVersionKey(song.AlbumID.String(), song.ID.String(), assetID, path.Ext(rawURL))
	}
	return storage.BuildMusicSongAudioVersionKey(song.ID.String(), assetID, path.Ext(rawURL))
}

func songCoverKey(song model.Song, rawURL string) string {
	assetID := migratedAssetID(rawURL, song.ID.String())
	if song.AlbumID != nil {
		return storage.BuildMusicAlbumCoverVersionKey(song.AlbumID.String(), assetID, path.Ext(rawURL))
	}
	return storage.BuildMusicSongCoverVersionKey(song.ID.String(), assetID, path.Ext(rawURL))
}

func rewriteRevisionSnapshot(p *planner, revision model.Revision, songs map[uuid.UUID]model.Song) ([]byte, bool, error) {
	var snapshot map[string]any
	if err := json.Unmarshal(revision.ContentSnapshot, &snapshot); err != nil {
		return nil, false, err
	}
	changed := false
	relocateField := func(target map[string]any, field, key string) {
		raw, _ := target[field].(string)
		if next := p.relocate(raw, key); next != raw {
			target[field] = next
			changed = true
		}
	}
	switch revision.ContentType {
	case "album":
		if album, ok := snapshot["album"].(map[string]any); ok {
			raw, _ := album["cover_url"].(string)
			relocateField(album, "cover_url", storage.BuildMusicAlbumCoverVersionKey(revision.ContentID.String(), migratedAssetID(raw, revision.ContentID.String()), path.Ext(raw)))
		}
		if rawSongs, ok := snapshot["songs"].([]any); ok {
			for _, rawSong := range rawSongs {
				songSnapshot, ok := rawSong.(map[string]any)
				if !ok {
					continue
				}
				songID, err := uuid.Parse(strings.TrimSpace(stringValue(songSnapshot["id"])))
				if err != nil {
					continue
				}
				song := songs[songID]
				song.ID, song.AlbumID = songID, &revision.ContentID
				rawAudio, _ := songSnapshot["audio_url"].(string)
				rawCover, _ := songSnapshot["cover_url"].(string)
				relocateField(songSnapshot, "audio_url", songAudioKey(song, rawAudio))
				relocateField(songSnapshot, "cover_url", songCoverKey(song, rawCover))
			}
		}
	case "song":
		song := songs[revision.ContentID]
		song.ID = revision.ContentID
		rawAudio, _ := snapshot["audio_url"].(string)
		rawCover, _ := snapshot["cover_url"].(string)
		relocateField(snapshot, "audio_url", songAudioKey(song, rawAudio))
		relocateField(snapshot, "cover_url", songCoverKey(song, rawCover))
	case "artist":
		raw, _ := snapshot["image_url"].(string)
		relocateField(snapshot, "image_url", storage.BuildMusicArtistImageVersionKey(revision.ContentID.String(), migratedAssetID(raw, revision.ContentID.String()), path.Ext(raw)))
	}
	if !changed {
		return revision.ContentSnapshot, false, nil
	}
	next, err := json.Marshal(snapshot)
	return next, err == nil, err
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func planTemporaryDeletes(db *gorm.DB, plan *cleanupPlan, objects map[string]objectInfo, now time.Time) error {
	var sessions []model.AlbumImportSession
	if err := db.Preload("Files").Find(&sessions).Error; err != nil {
		return err
	}
	sessionByID := make(map[uuid.UUID]model.AlbumImportSession, len(sessions))
	for _, session := range sessions {
		sessionByID[session.ID] = session
	}
	for key, item := range objects {
		if strings.HasPrefix(key, "music/covers/") || strings.HasPrefix(key, "music/audio/uploads/") {
			plan.DeleteKeys[key] = item
			continue
		}
		sessionID, ok := importSessionID(key)
		if !ok {
			if item.LastModified.Before(now.Add(-7 * 24 * time.Hour)) {
				plan.DeleteKeys[key] = item
			}
			continue
		}
		session, exists := sessionByID[sessionID]
		if !exists {
			if item.LastModified.Before(now.Add(-7 * 24 * time.Hour)) {
				plan.DeleteKeys[key] = item
			}
			continue
		}
		switch session.Status {
		case "committed", "canceled":
			plan.DeleteKeys[key] = item
		case "ready":
			if strings.HasPrefix(key, "music/album-imports/source/") && importSessionParsedSuccessfully(session) {
				plan.DeleteKeys[key] = item
			}
		}
	}
	return nil
}

func importSessionParsedSuccessfully(session model.AlbumImportSession) bool {
	if len(session.Files) == 0 {
		return false
	}
	for _, file := range session.Files {
		if file.ProcessingStatus == "failed" || strings.TrimSpace(file.ErrorMessage) != "" {
			return false
		}
	}
	return true
}

func importSessionID(key string) (uuid.UUID, bool) {
	parts := strings.Split(key, "/")
	for index := range parts {
		if parts[index] != "sessions" || index+1 >= len(parts) {
			continue
		}
		id, err := uuid.Parse(parts[index+1])
		return id, err == nil
	}
	return uuid.Nil, false
}

func applyCopies(client *s3.S3, bucket string, copies map[string]copyItem) error {
	for _, item := range copies {
		escapedSource := strings.ReplaceAll(url.PathEscape(bucket+"/"+item.Source), "%2F", "/")
		if _, err := client.CopyObject(&s3.CopyObjectInput{Bucket: aws.String(bucket), CopySource: aws.String(escapedSource), Key: aws.String(item.Target)}); err != nil {
			return fmt.Errorf("%s -> %s: %w", item.Source, item.Target, err)
		}
		head, err := client.HeadObject(&s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(item.Target)})
		if err != nil {
			return err
		}
		if aws.Int64Value(head.ContentLength) != item.Size {
			return fmt.Errorf("copied object size mismatch: %s -> %s", item.Source, item.Target)
		}
	}
	return nil
}

func applyDatabaseUpdates(db *gorm.DB, plan *cleanupPlan) error {
	return db.Transaction(func(tx *gorm.DB) error {
		for id, value := range plan.AlbumCovers {
			if err := tx.Model(&model.Album{}).Where("id = ?", id).Update("cover_url", value).Error; err != nil {
				return err
			}
		}
		for id, value := range plan.SongAudio {
			if err := tx.Model(&model.Song{}).Where("id = ?", id).Update("audio_url", value).Error; err != nil {
				return err
			}
		}
		for id, value := range plan.SongCovers {
			if err := tx.Model(&model.Song{}).Where("id = ?", id).Update("cover_url", value).Error; err != nil {
				return err
			}
		}
		for id, value := range plan.ArtistImages {
			if err := tx.Model(&model.Artist{}).Where("id = ?", id).Update("image_url", value).Error; err != nil {
				return err
			}
		}
		for id, value := range plan.PlaylistCovers {
			if err := tx.Model(&model.Playlist{}).Where("id = ?", id).Update("cover_url", value).Error; err != nil {
				return err
			}
		}
		for id, value := range plan.RevisionSnapshots {
			if err := tx.Model(&model.Revision{}).Where("id = ?", id).Update("content_snapshot", value).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func collectTemporaryReferenceKeys(db *gorm.DB, prefix string) (map[string]bool, error) {
	objects := map[string]objectInfo{}
	plan, err := buildCleanupPlan(db, prefix, objects, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return plan.ReferencedTempKeys, nil
}

func deleteObjects(client *s3.S3, bucket string, objects map[string]objectInfo) error {
	for key := range objects {
		if _, err := client.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
			return fmt.Errorf("delete %s: %w", key, err)
		}
	}
	return nil
}

func printPlan(plan *cleanupPlan) {
	var copyBytes, deleteBytes int64
	for _, item := range plan.Copies {
		copyBytes += item.Size
	}
	for _, item := range plan.DeleteKeys {
		deleteBytes += item.Size
	}
	log.Printf("planned copies: %d objects, %d bytes", len(plan.Copies), copyBytes)
	log.Printf("planned database updates: albums=%d songs_audio=%d songs_cover=%d artists=%d playlists=%d revisions=%d",
		len(plan.AlbumCovers), len(plan.SongAudio), len(plan.SongCovers), len(plan.ArtistImages), len(plan.PlaylistCovers), len(plan.RevisionSnapshots))
	log.Printf("planned deletions: %d objects, %d bytes", len(plan.DeleteKeys), deleteBytes)
	log.Printf("missing referenced objects: %d", len(plan.MissingReferences))
}
