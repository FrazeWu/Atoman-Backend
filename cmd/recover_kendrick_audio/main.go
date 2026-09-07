package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/model"
	"atoman/internal/storage"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

var featureSuffixPattern = regexp.MustCompile(`(?i)\s*[\(\[]\s*(feat(?:uring)?\.?|with)\b.*?[\)\]]`)

type trackCandidate struct {
	ID          uuid.UUID
	AlbumID     uuid.UUID
	Title       string
	DiscNumber  int
	TrackNumber int
	ObjectKey   string
}

type objectInfo struct {
	Key          string
	LastModified time.Time
}

type repairItem struct {
	TargetID       uuid.UUID
	TargetAlbum    string
	TargetTitle    string
	TargetDisc     int
	TargetTrack    int
	TargetAudioURL string
	SourceID       uuid.UUID
	SourceAlbumID  uuid.UUID
	SourceTitle    string
	ObjectKey      string
	PlaybackURL    string
	MatchReason    string
}

type repairStats struct {
	ActiveAlbums int
	ActiveSongs  int
	MissingAudio int
	Matched      int
	NoCandidate  int
	Ambiguous    int
	Objects      int
}

func main() {
	envFile := flag.String("env", ".env.prod", "environment file")
	artistName := flag.String("artist", "Kendrick Lamar", "artist name")
	apply := flag.Bool("apply", false, "write audio references to active songs")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		log.Fatalf("load env: %v", err)
	}
	db, err := app.OpenDB(config.DBConfig{Type: os.Getenv("DATABASE_TYPE"), URL: os.Getenv("DATABASE_URL")})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	client, err := storage.InitS3Client()
	if err != nil {
		log.Fatalf("init object storage: %v", err)
	}
	objects, objectsBySong, err := listAudioObjects(client, strings.TrimSpace(os.Getenv("S3_BUCKET")))
	if err != nil {
		log.Fatalf("list audio objects: %v", err)
	}

	plans, stats, err := buildRepairPlan(db, strings.TrimSpace(*artistName), objects, objectsBySong)
	if err != nil {
		log.Fatalf("build repair plan: %v", err)
	}
	printPlan(strings.TrimSpace(*artistName), plans, stats)
	if !*apply {
		log.Println("dry run only; rerun with -apply after reviewing the mapping")
		return
	}
	if err := applyRepairPlan(db, plans); err != nil {
		log.Fatalf("apply repair plan: %v", err)
	}
	log.Printf("audio recovery applied: updated=%d", len(plans))
}

func normalizeTrackTitle(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.NewReplacer("’", "'", "‘", "'", "＇", "'").Replace(raw)
	raw = featureSuffixPattern.ReplaceAllString(raw, " ")
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, raw)), " ")
}

func chooseAudioCandidate(target trackCandidate, candidates []trackCandidate) (trackCandidate, bool, string) {
	disc := target.DiscNumber
	if disc <= 0 {
		disc = 1
	}
	if target.TrackNumber <= 0 {
		return trackCandidate{}, false, "missing_track_number"
	}
	title := normalizeTrackTitle(target.Title)
	matches := make([]trackCandidate, 0, len(candidates))
	seen := map[uuid.UUID]struct{}{}
	for _, candidate := range candidates {
		candidateDisc := candidate.DiscNumber
		if candidateDisc <= 0 {
			candidateDisc = 1
		}
		if candidateDisc != disc || candidate.TrackNumber != target.TrackNumber || normalizeTrackTitle(candidate.Title) != title || candidate.ObjectKey == "" {
			continue
		}
		if _, ok := seen[candidate.ID]; ok {
			continue
		}
		seen[candidate.ID] = struct{}{}
		matches = append(matches, candidate)
	}
	if len(matches) == 0 {
		titleMatches := make([]trackCandidate, 0, len(candidates))
		seen = map[uuid.UUID]struct{}{}
		for _, candidate := range candidates {
			if normalizeTrackTitle(candidate.Title) != title || candidate.ObjectKey == "" {
				continue
			}
			if _, ok := seen[candidate.ID]; ok {
				continue
			}
			seen[candidate.ID] = struct{}{}
			titleMatches = append(titleMatches, candidate)
		}
		if len(titleMatches) == 1 {
			return titleMatches[0], true, "album_title"
		}
		if len(titleMatches) > 1 {
			return trackCandidate{}, false, "ambiguous"
		}
		return trackCandidate{}, false, "not_found"
	}
	if len(matches) > 1 {
		return trackCandidate{}, false, "ambiguous"
	}
	return matches[0], true, "album_disc_track_title"
}

func normalizeAlbumTitle(raw string) string {
	key := normalizeTrackTitle(raw)
	for _, suffix := range []string{" deluxe version", " deluxe edition", " deluxe"} {
		if strings.HasSuffix(key, suffix) {
			return strings.TrimSpace(strings.TrimSuffix(key, suffix))
		}
	}
	return key
}

func listAudioObjects(client *s3.S3, bucket string) (map[string]objectInfo, map[uuid.UUID][]objectInfo, error) {
	objects := map[string]objectInfo{}
	objectsBySong := map[uuid.UUID][]objectInfo{}
	for _, prefix := range []string{"music/albums/", "music/songs/"} {
		input := &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix)}
		for {
			output, err := client.ListObjectsV2(input)
			if err != nil {
				return nil, nil, err
			}
			for _, item := range output.Contents {
				key := aws.StringValue(item.Key)
				songID, ok := songIDFromAudioKey(key)
				if !ok {
					continue
				}
				info := objectInfo{Key: key, LastModified: aws.TimeValue(item.LastModified)}
				objects[key] = info
				objectsBySong[songID] = append(objectsBySong[songID], info)
			}
			if !aws.BoolValue(output.IsTruncated) {
				break
			}
			input.ContinuationToken = output.NextContinuationToken
		}
	}
	for songID := range objectsBySong {
		sort.Slice(objectsBySong[songID], func(i, j int) bool {
			left, right := objectsBySong[songID][i], objectsBySong[songID][j]
			if left.LastModified.Equal(right.LastModified) {
				return left.Key > right.Key
			}
			return left.LastModified.After(right.LastModified)
		})
	}
	return objects, objectsBySong, nil
}

func songIDFromAudioKey(key string) (uuid.UUID, bool) {
	parts := strings.Split(strings.Trim(key, "/"), "/")
	if len(parts) >= 6 && parts[0] == "music" && parts[1] == "albums" && parts[3] == "tracks" {
		id, err := uuid.Parse(parts[4])
		return id, err == nil
	}
	if len(parts) >= 5 && parts[0] == "music" && parts[1] == "songs" && parts[3] == "audio" {
		id, err := uuid.Parse(parts[2])
		return id, err == nil
	}
	return uuid.Nil, false
}

func albumIDFromAudioKey(key string) (uuid.UUID, bool) {
	parts := strings.Split(strings.Trim(key, "/"), "/")
	if len(parts) >= 6 && parts[0] == "music" && parts[1] == "albums" && parts[3] == "tracks" {
		id, err := uuid.Parse(parts[2])
		return id, err == nil
	}
	return uuid.Nil, false
}

func buildRepairPlan(db *gorm.DB, artistName string, objects map[string]objectInfo, objectsBySong map[uuid.UUID][]objectInfo) ([]repairItem, repairStats, error) {
	debug := strings.TrimSpace(os.Getenv("RECOVER_MUSIC_DEBUG")) == "1"
	var artist model.Artist
	if err := db.Where("LOWER(name) = ? AND deleted_at IS NULL", strings.ToLower(artistName)).First(&artist).Error; err != nil {
		return nil, repairStats{}, fmt.Errorf("find artist %q: %w", artistName, err)
	}

	var activeAlbums []model.Album
	if err := db.Model(&model.Album{}).
		Where("id IN (?) AND deleted_at IS NULL AND lifecycle_status = ?", db.Table("album_artists").Select("album_id").Where("artist_id = ?", artist.ID), model.MusicLifecycleActive).
		Order("release_date, title, id").Find(&activeAlbums).Error; err != nil {
		return nil, repairStats{}, fmt.Errorf("load active albums: %w", err)
	}

	var activeSongs []model.Song
	activeAlbumIDs := make([]uuid.UUID, 0, len(activeAlbums))
	for _, album := range activeAlbums {
		activeAlbumIDs = append(activeAlbumIDs, album.ID)
	}
	if err := db.Where("album_id IN ? AND deleted_at IS NULL AND lifecycle_status = ?", activeAlbumIDs, model.MusicLifecycleActive).
		Order("album_id, disc_number, track_number, id").Find(&activeSongs).Error; err != nil {
		return nil, repairStats{}, fmt.Errorf("load active songs: %w", err)
	}

	var relatedAlbumIDs []uuid.UUID
	if err := db.Table("album_artists").Where("artist_id = ?", artist.ID).Pluck("album_id", &relatedAlbumIDs).Error; err != nil {
		return nil, repairStats{}, fmt.Errorf("load related album ids: %w", err)
	}
	sourceAlbumIDs := make(map[uuid.UUID]struct{}, len(relatedAlbumIDs))
	for _, id := range relatedAlbumIDs {
		sourceAlbumIDs[id] = struct{}{}
	}
	for key := range objects {
		if albumID, ok := albumIDFromAudioKey(key); ok {
			sourceAlbumIDs[albumID] = struct{}{}
		}
	}
	sourceAlbumIDList := make([]uuid.UUID, 0, len(sourceAlbumIDs))
	for id := range sourceAlbumIDs {
		sourceAlbumIDList = append(sourceAlbumIDList, id)
	}
	var sourceAlbums []model.Album
	if err := db.Unscoped().Where("id IN ?", sourceAlbumIDList).Find(&sourceAlbums).Error; err != nil {
		return nil, repairStats{}, fmt.Errorf("load source albums: %w", err)
	}
	sourceAlbumsByTitle := make(map[string][]model.Album)
	for _, album := range sourceAlbums {
		key := normalizeAlbumTitle(album.Title)
		sourceAlbumsByTitle[key] = append(sourceAlbumsByTitle[key], album)
	}
	if debug {
		log.Printf("active albums=%d source albums=%d source title groups=%d", len(activeAlbums), len(sourceAlbums), len(sourceAlbumsByTitle))
		for _, album := range activeAlbums {
			log.Printf("album title=%q id=%s source_same_title=%d", album.Title, album.ID, len(sourceAlbumsByTitle[normalizeTrackTitle(album.Title)]))
		}
	}

	var sourceSongs []model.Song
	if err := db.Unscoped().Where("album_id IN ?", sourceAlbumIDList).Find(&sourceSongs).Error; err != nil {
		return nil, repairStats{}, fmt.Errorf("load source songs: %w", err)
	}
	sourceCandidatesByAlbum := make(map[uuid.UUID][]trackCandidate)
	for _, song := range sourceSongs {
		if song.AlbumID == nil || strings.TrimSpace(song.Title) == "" {
			continue
		}
		objectKey := sourceObjectKey(song, objects, objectsBySong)
		if objectKey == "" {
			continue
		}
		sourceCandidatesByAlbum[*song.AlbumID] = append(sourceCandidatesByAlbum[*song.AlbumID], trackCandidate{
			ID: song.ID, AlbumID: *song.AlbumID, Title: song.Title, DiscNumber: song.DiscNumber, TrackNumber: song.TrackNumber, ObjectKey: objectKey,
		})
	}
	if debug {
		for _, album := range activeAlbums {
			for _, sourceAlbum := range sourceAlbumsByTitle[normalizeAlbumTitle(album.Title)] {
				log.Printf("source album title=%q id=%s deleted=%t candidates=%d", sourceAlbum.Title, sourceAlbum.ID, sourceAlbum.DeletedAt.Valid, len(sourceCandidatesByAlbum[sourceAlbum.ID]))
			}
		}
	}

	currentAlbumTitles := make(map[uuid.UUID]string, len(activeAlbums))
	for _, album := range activeAlbums {
		currentAlbumTitles[album.ID] = album.Title
	}
	plans := make([]repairItem, 0)
	stats := repairStats{ActiveAlbums: len(activeAlbums), Objects: len(objects)}
	for _, song := range activeSongs {
		if !needsAudioRecovery(song.AudioURL, objects) {
			continue
		}
		stats.MissingAudio++
		if song.AlbumID == nil {
			stats.NoCandidate++
			continue
		}
		candidates := make([]trackCandidate, 0)
		for _, sourceAlbum := range sourceAlbumsByTitle[normalizeAlbumTitle(currentAlbumTitles[*song.AlbumID])] {
			candidates = append(candidates, sourceCandidatesByAlbum[sourceAlbum.ID]...)
		}
		match, ok, reason := chooseAudioCandidate(trackCandidate{ID: song.ID, AlbumID: *song.AlbumID, Title: song.Title, DiscNumber: song.DiscNumber, TrackNumber: song.TrackNumber}, candidates)
		if debug {
			log.Printf("song album=%q disc=%d track=%d title=%q candidates=%d matched=%t reason=%s", currentAlbumTitles[*song.AlbumID], normalizedDisc(song.DiscNumber), song.TrackNumber, song.Title, len(candidates), ok, reason)
		}
		if !ok {
			if debug {
				for _, candidate := range candidates {
					if strings.Contains(normalizeTrackTitle(song.Title), "heart") {
						log.Printf("  candidate disc=%d track=%d title=%q source=%s", normalizedDisc(candidate.DiscNumber), candidate.TrackNumber, candidate.Title, candidate.ID)
					}
					if normalizeTrackTitle(candidate.Title) == normalizeTrackTitle(song.Title) {
						log.Printf("  same title candidate disc=%d track=%d title=%q source=%s", normalizedDisc(candidate.DiscNumber), candidate.TrackNumber, candidate.Title, candidate.ID)
					}
				}
			}
			if reason == "ambiguous" {
				stats.Ambiguous++
			} else {
				stats.NoCandidate++
			}
			continue
		}
		stats.Matched++
		plans = append(plans, repairItem{
			TargetID: song.ID, TargetAlbum: currentAlbumTitles[*song.AlbumID], TargetTitle: song.Title,
			TargetDisc: song.DiscNumber, TargetTrack: song.TrackNumber, TargetAudioURL: song.AudioURL,
			SourceID: match.ID, SourceAlbumID: match.AlbumID, SourceTitle: match.Title, ObjectKey: match.ObjectKey,
			PlaybackURL: buildPlaybackURL(match.ObjectKey), MatchReason: reason,
		})
	}
	return plans, stats, nil
}

func sourceObjectKey(song model.Song, objects map[string]objectInfo, objectsBySong map[uuid.UUID][]objectInfo) string {
	for _, raw := range []string{song.AudioURL, song.PlaybackKey, song.SourceKey} {
		key := configuredObjectKey(raw)
		if key != "" {
			if _, ok := objects[key]; ok {
				return key
			}
		}
	}
	if candidates := objectsBySong[song.ID]; len(candidates) > 0 {
		return candidates[0].Key
	}
	return ""
}

func configuredObjectKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	for _, prefix := range []string{os.Getenv("S3_URL_PREFIX"), os.Getenv("MUSIC_PLAYBACK_URL_PREFIX")} {
		prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
		if prefix != "" && strings.HasPrefix(raw, prefix+"/") {
			return strings.TrimLeft(strings.TrimPrefix(raw, prefix+"/"), "/")
		}
	}
	if strings.HasPrefix(raw, "music/") {
		return strings.TrimLeft(raw, "/")
	}
	return ""
}

func needsAudioRecovery(raw string, objects map[string]objectInfo) bool {
	if strings.TrimSpace(raw) == "" {
		return true
	}
	key := configuredObjectKey(raw)
	return key != "" && objects[key].Key == ""
}

func buildPlaybackURL(key string) string {
	prefix := strings.TrimRight(strings.TrimSpace(os.Getenv("MUSIC_PLAYBACK_URL_PREFIX")), "/")
	if prefix == "" {
		prefix = strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
	}
	return prefix + "/" + strings.TrimLeft(key, "/")
}

func applyRepairPlan(db *gorm.DB, plans []repairItem) error {
	return db.Transaction(func(tx *gorm.DB) error {
		for _, item := range plans {
			result := tx.Model(&model.Song{}).
				Where("id = ? AND deleted_at IS NULL AND audio_url = ?", item.TargetID, item.TargetAudioURL).
				Updates(map[string]any{"audio_url": item.PlaybackURL, "audio_source": "s3"})
			if result.Error != nil {
				return fmt.Errorf("update song %s: %w", item.TargetID, result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("song %s changed or disappeared after dry run", item.TargetID)
			}
		}
		return nil
	})
}

func printPlan(artistName string, plans []repairItem, stats repairStats) {
	fmt.Printf("artist=%s active_albums=%d objects=%d missing_audio=%d matched=%d no_candidate=%d ambiguous=%d\n", artistName, stats.ActiveAlbums, stats.Objects, stats.MissingAudio, stats.Matched, stats.NoCandidate, stats.Ambiguous)
	for _, item := range plans {
		fmt.Printf("MATCH %s | %d.%02d | %s -> source=%s source_album=%s | %s | %s\n", item.TargetAlbum, normalizedDisc(item.TargetDisc), item.TargetTrack, item.TargetTitle, item.SourceID, item.SourceAlbumID, item.ObjectKey, item.MatchReason)
	}
}

func normalizedDisc(value int) int {
	if value <= 0 {
		return 1
	}
	return value
}
