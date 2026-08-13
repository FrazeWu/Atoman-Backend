package music

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/service"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type CatalogMetadataBackfillResult struct {
	AlbumID        uuid.UUID
	AlbumTitle     string
	MatchedTitle   string
	SourceURL      string
	Matched        bool
	Applied        bool
	LyricsAdded    int
	MissingArtists []string
	Reason         string
}

func StripCatalogSongArtistPrefixes(ctx context.Context, db *gorm.DB) (int64, error) {
	var songs []model.Song
	if err := db.WithContext(ctx).Where("title LIKE ?", "% - %").Find(&songs).Error; err != nil {
		return 0, err
	}
	albumSongs := make(map[uuid.UUID][]model.Song)
	for _, song := range songs {
		if song.AlbumID != nil && strippedCatalogSongTitle(song.Title) != song.Title {
			albumSongs[*song.AlbumID] = append(albumSongs[*song.AlbumID], song)
		}
	}
	var updated int64
	for albumID, matches := range albumSongs {
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var album model.Album
			if err := tx.First(&album, "id = ?", albumID).Error; err != nil {
				return err
			}
			if album.UploadedBy == nil || *album.UploadedBy == uuid.Nil {
				return fmt.Errorf("album %s has no uploader for revision attribution", albumID)
			}
			revisions := service.NewRevisionService(tx)
			if _, err := revisions.EnsureInitialRevision("album", albumID, *album.UploadedBy); err != nil {
				return err
			}
			for _, song := range matches {
				if err := tx.Model(&model.Song{}).Where("id = ?", song.ID).Update("title", strippedCatalogSongTitle(song.Title)).Error; err != nil {
					return err
				}
				updated++
			}
			_, err := revisions.CreateCurrentSnapshotRevision("album", albumID, *album.UploadedBy, "清理歌曲标题中的艺术家前缀")
			return err
		})
		if err != nil {
			return updated, err
		}
	}
	return updated, nil
}

func strippedCatalogSongTitle(title string) string {
	if index := strings.LastIndex(title, " - "); index > 0 && index+3 < len(title) {
		return strings.TrimSpace(title[index+3:])
	}
	return title
}

func StripCatalogSongTitlePrefix(ctx context.Context, db *gorm.DB, prefix string) (int64, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return 0, nil
	}
	var songs []model.Song
	if err := db.WithContext(ctx).Where("title LIKE ?", prefix+"%").Find(&songs).Error; err != nil {
		return 0, err
	}
	albumSongs := make(map[uuid.UUID][]model.Song)
	for _, song := range songs {
		if song.AlbumID != nil {
			albumSongs[*song.AlbumID] = append(albumSongs[*song.AlbumID], song)
		}
	}
	var updated int64
	for albumID, matches := range albumSongs {
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var album model.Album
			if err := tx.First(&album, "id = ?", albumID).Error; err != nil {
				return err
			}
			if album.UploadedBy == nil || *album.UploadedBy == uuid.Nil {
				return fmt.Errorf("album %s has no uploader for revision attribution", albumID)
			}
			revisions := service.NewRevisionService(tx)
			if _, err := revisions.EnsureInitialRevision("album", albumID, *album.UploadedBy); err != nil {
				return err
			}
			for _, song := range matches {
				title := strings.TrimSpace(strings.TrimPrefix(song.Title, prefix))
				if title == "" {
					return fmt.Errorf("song %s title would become empty", song.ID)
				}
				if err := tx.Model(&model.Song{}).Where("id = ?", song.ID).Update("title", title).Error; err != nil {
					return err
				}
				updated++
			}
			_, err := revisions.CreateCurrentSnapshotRevision("album", albumID, *album.UploadedBy, "清理歌曲标题中的艺术家前缀")
			return err
		})
		if err != nil {
			return updated, err
		}
	}
	return updated, nil
}

func BackfillCatalogMetadata(ctx context.Context, db *gorm.DB, userAgent string, apply bool, options ...string) ([]CatalogMetadataBackfillResult, error) {
	var albums []model.Album
	query := db.WithContext(ctx).
		Preload("Artists").
		Preload("ArtistCredits", func(db *gorm.DB) *gorm.DB { return db.Order("position ASC").Order("created_at ASC") }).
		Preload("ArtistCredits.Artist").
		Preload("ArtistCredits.Artist.Aliases").
		Preload("Songs").Where("redirect_to IS NULL")
	if len(options) > 0 && strings.TrimSpace(options[0]) != "" {
		query = query.Where("id = ?", strings.TrimSpace(options[0]))
	}
	if err := query.Order("created_at ASC").Find(&albums).Error; err != nil {
		return nil, err
	}
	enricher := NewExternalAlbumMetadataEnricher(&http.Client{Timeout: 10 * time.Second}, "https://musicbrainz.org", "https://coverartarchive.org", "https://lrclib.net", userAgent)
	results := make([]CatalogMetadataBackfillResult, 0, len(albums))
	for index := range albums {
		preferredReleaseID := ""
		if len(options) > 1 {
			preferredReleaseID = strings.TrimSpace(options[1])
		}
		result := backfillCatalogAlbum(ctx, db, enricher, &albums[index], apply, preferredReleaseID)
		results = append(results, result)
	}
	return results, nil
}

func backfillCatalogAlbum(ctx context.Context, db *gorm.DB, enricher *ExternalAlbumMetadataEnricher, album *model.Album, apply bool, preferredReleaseIDs ...string) CatalogMetadataBackfillResult {
	result := CatalogMetadataBackfillResult{AlbumID: album.ID, AlbumTitle: album.Title}
	artist := ""
	artists := []string{}
	for _, credit := range album.ArtistCredits {
		if credit.Artist != nil {
			artists = append(artists, musicArtistSearchNames(*credit.Artist)...)
		}
	}
	if len(artists) == 0 {
		for _, item := range album.Artists {
			artists = append(artists, item.Name)
		}
	}
	artists = uniqueMusicArtists(artists)
	if len(artists) > 0 {
		artist = artists[0]
	}
	tracks := make([]AlbumImportMetadataTrack, 0, len(album.Songs))
	for _, song := range album.Songs {
		title := existingSongSourceTitle(song, artist)
		duration := float64(song.DurationSec)
		if duration <= 0 {
			duration = probeExistingSongDuration(ctx, song.AudioURL)
			if duration > 0 {
				_ = db.WithContext(ctx).Model(&model.Song{}).Where("id = ?", song.ID).Update("duration_sec", int(duration+0.5)).Error
			}
		}
		tracks = append(tracks, AlbumImportMetadataTrack{
			Title: title, Artist: artist, DiscNumber: song.DiscNumber, TrackNumber: song.TrackNumber, DurationSeconds: duration,
			Origin: song.ID.String(), AudioKey: song.ID.String(), AudioURL: song.AudioURL,
		})
	}
	preferredReleaseID := ""
	if len(preferredReleaseIDs) > 0 {
		preferredReleaseID = strings.TrimSpace(preferredReleaseIDs[0])
	}
	if album.ID.String() == "019fa2ca-a4b3-7288-82c2-7b2384c40b80" {
		preferredReleaseID = "da13b81f-7b09-3fb6-b5c9-8551f22c797e"
	}
	enriched, err := enricher.Enrich(ctx, AlbumImportMetadataInput{AlbumTitle: album.Title, Artist: artist, Artists: artists, PreferredReleaseID: preferredReleaseID, Tracks: tracks})
	if err != nil || enriched.SourceURL == "" {
		if err != nil {
			result.Reason = err.Error()
		} else if enriched.MetadataError != "" {
			result.Reason = enriched.MetadataError
		} else {
			result.Reason = "MusicBrainz has no safe matching release"
		}
		return result
	}
	result.Matched = true
	result.MatchedTitle = enriched.AlbumTitle
	result.SourceURL = enriched.SourceURL
	result.MissingArtists = enriched.MissingArtists
	if !apply {
		return result
	}
	actorID := uuid.Nil
	if album.UploadedBy != nil {
		actorID = *album.UploadedBy
	}
	if actorID == uuid.Nil {
		result.Reason = "album has no uploader for revision attribution"
		return result
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		revisions := service.NewRevisionService(tx)
		if _, err := revisions.EnsureInitialRevision("album", album.ID, actorID); err != nil {
			return err
		}
		sources := appendMusicBrainzSource(album.Sources, enriched.SourceURL)
		sourcesJSON, err := json.Marshal(sources)
		if err != nil {
			return err
		}
		updates := map[string]any{"title": enriched.AlbumTitle, "album_type": enriched.AlbumType, "sources_json": string(sourcesJSON)}
		if date, precision, ok := parseBackfillReleaseDate(enriched.ReleaseDate); ok {
			updates["release_date"] = date
			updates["release_date_precision"] = precision
			updates["release_year"] = date.Year()
			updates["year"] = date.Year()
		}
		if album.CoverURL == "" && enriched.CoverURL != "" {
			updates["cover_url"] = enriched.CoverURL
			updates["cover_source"] = "external"
		}
		if err := tx.Model(&model.Album{}).Where("id = ?", album.ID).Updates(updates).Error; err != nil {
			return err
		}
		for _, track := range enriched.Tracks {
			songID, err := uuid.Parse(track.AudioKey)
			if err != nil {
				return err
			}
			var song model.Song
			if err := tx.First(&song, "id = ? AND album_id = ?", songID, album.ID).Error; err != nil {
				return err
			}
			if err := tx.Model(&song).Updates(map[string]any{"title": track.Title, "disc_number": track.DiscNumber, "track_number": track.TrackNumber}).Error; err != nil {
				return err
			}
			if strings.TrimSpace(song.Lyrics) == "" && track.Lyrics != nil && strings.TrimSpace(track.Lyrics.Content) != "" {
				if err := SyncLegacySongLyricsWithFormat(tx, actorID, song.ID, track.Lyrics.Content, track.Lyrics.Translation, track.Lyrics.Format, "自动匹配歌词"); err != nil {
					return err
				}
				result.LyricsAdded++
			}
		}
		_, err = revisions.CreateCurrentSnapshotRevision("album", album.ID, actorID, "自动匹配外部音乐信息")
		return err
	})
	if err != nil {
		result.Reason = err.Error()
		result.LyricsAdded = 0
		return result
	}
	result.Applied = true
	return result
}

func musicArtistSearchNames(artist model.Artist) []string {
	names := []string{artist.Name, artist.LegalName}
	for _, alias := range artist.Aliases {
		names = append(names, alias.Alias)
	}
	return uniqueMusicArtists(names)
}

func existingSongSourceTitle(song model.Song, artist string) string {
	sourceFileName := strings.TrimSpace(song.SourceFileName)
	if sourceFileName == "" {
		return song.Title
	}
	title := strings.TrimSpace(strings.TrimSuffix(filepath.Base(sourceFileName), filepath.Ext(sourceFileName)))
	if parsed, _, _ := albumImportTrackInfoFromFileName(title); parsed != "" {
		title = parsed
	}
	prefix := artist + " - "
	if strings.HasPrefix(strings.ToLower(title), strings.ToLower(prefix)) {
		title = strings.TrimSpace(title[len(prefix):])
	}
	suffix := "-" + artist
	if artist != "" && strings.HasSuffix(strings.ToLower(title), strings.ToLower(suffix)) {
		title = strings.TrimSpace(title[:len(title)-len(suffix)])
	}
	if title == "" {
		return song.Title
	}
	return title
}

func probeExistingSongDuration(ctx context.Context, audioURL string) float64 {
	output, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", audioURL).Output()
	if err != nil {
		return 0
	}
	duration, _ := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	return duration
}

func appendMusicBrainzSource(sources []model.MusicSource, sourceURL string) []model.MusicSource {
	result := append([]model.MusicSource(nil), sources...)
	for _, source := range result {
		if source.URL == sourceURL {
			return result
		}
	}
	return append(result, model.MusicSource{Type: "url", URL: sourceURL, Title: "MusicBrainz"})
}

func parseBackfillReleaseDate(value string) (time.Time, string, bool) {
	for _, candidate := range []struct {
		layout, precision string
	}{{"2006-01-02", "day"}, {"2006-01", "month"}, {"2006", "year"}} {
		if parsed, err := time.Parse(candidate.layout, value); err == nil {
			return parsed, candidate.precision, true
		}
	}
	return time.Time{}, "", false
}

func FormatCatalogMetadataBackfillResult(result CatalogMetadataBackfillResult) string {
	status := "SKIP"
	if result.Matched {
		status = "MATCH"
	}
	if result.Applied {
		status = "APPLIED"
	}
	return fmt.Sprintf("%s\t%s\t%s => %s\tlyrics=%d\tsource=%s\tmissing_artists=%s\t%s", status, result.AlbumID, result.AlbumTitle, result.MatchedTitle, result.LyricsAdded, result.SourceURL, strings.Join(result.MissingArtists, ","), result.Reason)
}
