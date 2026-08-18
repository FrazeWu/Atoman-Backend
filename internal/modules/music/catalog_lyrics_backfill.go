package music

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CatalogLyricsBackfillResult describes one attempted lyrics lookup for an existing song.
type CatalogLyricsBackfillResult struct {
	SongID          uuid.UUID
	SongTitle       string
	Artist          string
	AlbumTitle      string
	Matched         bool
	Applied         bool
	DurationSeconds float64
	Reason          string
}

// BackfillCatalogLyrics matches only songs without current lyrics. MusicBrainz must safely
// match the album and track before LRCLIB is queried.
func BackfillCatalogLyrics(ctx context.Context, db *gorm.DB, userAgent string, apply bool, songID string, preferredReleaseIDs ...string) ([]CatalogLyricsBackfillResult, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	var songs []model.Song
	query := db.WithContext(ctx).
		Preload("Album").
		Preload("Album.Artists").
		Preload("Album.ArtistCredits", func(db *gorm.DB) *gorm.DB { return db.Order("position ASC").Order("created_at ASC") }).
		Preload("Album.ArtistCredits.Artist").
		Preload("Artists").
		Preload("ArtistCredits", func(db *gorm.DB) *gorm.DB { return db.Order("position ASC").Order("created_at ASC") }).
		Preload("ArtistCredits.Artist").
		Where("COALESCE(lyrics, '') = ''").
		Where(`NOT EXISTS (
			SELECT 1 FROM music_song_lyrics AS current_lyrics
			WHERE current_lyrics.song_id = "Songs".id
			  AND current_lyrics.deleted_at IS NULL
			  AND BTRIM(COALESCE(current_lyrics.content, '')) <> ''
		)`)
	if strings.TrimSpace(songID) != "" {
		query = query.Where("id = ?", strings.TrimSpace(songID))
	}
	if err := query.Order("created_at ASC").Find(&songs).Error; err != nil {
		return nil, err
	}

	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		userAgent = "Atoman/1.0 (https://www.atoman.org)"
	}
	preferredReleaseID := ""
	if len(preferredReleaseIDs) > 0 {
		preferredReleaseID = strings.TrimSpace(preferredReleaseIDs[0])
	}
	enricher := NewExternalAlbumMetadataEnricher(&http.Client{Timeout: 15 * time.Second}, "https://musicbrainz.org", "https://coverartarchive.org", "https://lrclib.net", userAgent)
	results := make([]CatalogLyricsBackfillResult, len(songs))
	jobs := make(chan int)
	workerCount := 4
	if len(songs) < workerCount {
		workerCount = len(songs)
	}
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = backfillCatalogSongLyrics(ctx, db, enricher, &songs[index], apply, preferredReleaseID)
			}
		}()
	}
	for index := range songs {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return results, ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	return results, nil
}

func backfillCatalogSongLyrics(ctx context.Context, db *gorm.DB, enricher *ExternalAlbumMetadataEnricher, song *model.Song, apply bool, preferredReleaseID string) CatalogLyricsBackfillResult {
	result := CatalogLyricsBackfillResult{SongID: song.ID, SongTitle: song.Title}
	artists := catalogSongArtistNames(song)
	if len(artists) == 0 {
		result.Reason = "song has no artist metadata"
		return result
	}
	if song.Album == nil || strings.TrimSpace(song.Album.Title) == "" {
		result.Reason = "song has no album metadata for MusicBrainz lookup"
		return result
	}
	result.Artist = artists[0]
	result.AlbumTitle = song.Album.Title
	title := catalogSongSourceTitle(*song, result.Artist, artists)
	duration := float64(song.DurationSec)
	if duration <= 0 {
		duration = probeExistingSongDuration(ctx, song.AudioURL)
	}
	result.DurationSeconds = duration
	if apply && song.DurationSec <= 0 && duration > 0 {
		if err := db.WithContext(ctx).Model(&model.Song{}).Where("id = ? AND duration_sec <= 0", song.ID).Update("duration_sec", int(duration+0.5)).Error; err != nil {
			result.Reason = fmt.Sprintf("update duration: %v", err)
			return result
		}
	}
	enriched, err := enricher.Enrich(ctx, AlbumImportMetadataInput{
		AlbumTitle:         result.AlbumTitle,
		Artist:             result.Artist,
		Artists:            artists,
		PreferredReleaseID: preferredReleaseID,
		Tracks: []AlbumImportMetadataTrack{{
			Title: title, Artist: result.Artist, Album: result.AlbumTitle,
			DiscNumber: song.DiscNumber, TrackNumber: song.TrackNumber,
			DurationSeconds: duration, Origin: song.ID.String(),
			AudioKey: song.ID.String(), AudioURL: song.AudioURL,
		}},
	})
	if err != nil {
		result.Reason = fmt.Sprintf("MusicBrainz lookup failed: %v", err)
		return result
	}
	if enriched.SourceURL == "" {
		result.Reason = "MusicBrainz did not safely match album"
		if enriched.MetadataError != "" {
			result.Reason += ": " + enriched.MetadataError
		}
		return result
	}
	result.AlbumTitle = enriched.AlbumTitle
	if len(enriched.Tracks) == 0 || enriched.Tracks[0].Lyrics == nil || strings.TrimSpace(enriched.Tracks[0].Lyrics.Content) == "" {
		result.Reason = "MusicBrainz matched, but LRCLIB returned no lyrics"
		return result
	}
	lyrics := *enriched.Tracks[0].Lyrics
	result.Matched = true
	if !apply {
		return result
	}
	actorID := uuid.Nil
	if song.UploadedBy != nil {
		actorID = *song.UploadedBy
	} else if song.Album != nil && song.Album.UploadedBy != nil {
		actorID = *song.Album.UploadedBy
	}
	if actorID == uuid.Nil {
		result.Reason = "song has no uploader for lyric attribution"
		return result
	}
	applied := false
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Song
		if err := tx.Select("id", "lyrics", "duration_sec").First(&current, "id = ?", song.ID).Error; err != nil {
			return err
		}
		if strings.TrimSpace(current.Lyrics) != "" {
			return nil
		}
		var currentLyrics model.MusicSongLyric
		err := tx.Where("song_id = ?", song.ID).First(&currentLyrics).Error
		if err == nil && strings.TrimSpace(currentLyrics.Content) != "" {
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := SyncLegacySongLyricsWithFormat(tx, actorID, song.ID, lyrics.Content, lyrics.Translation, lyrics.Format, lyrics.EditSummary); err != nil {
			return err
		}
		if current.DurationSec <= 0 && duration > 0 {
			if err := tx.Model(&model.Song{}).Where("id = ?", song.ID).Update("duration_sec", int(duration+0.5)).Error; err != nil {
				return err
			}
		}
		applied = true
		return nil
	})
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	result.Applied = applied
	if !applied {
		result.Reason = "lyrics already exist"
	}
	return result
}

func catalogSongSourceTitle(song model.Song, artist string, artists []string) string {
	title := stripKnownArtistPrefix(existingSongSourceTitle(song, artist), artists)
	if separator := strings.LastIndex(title, " - "); separator > 0 {
		candidate := strings.TrimSpace(title[separator+3:])
		if comparableMusicTitle(candidate) == comparableMusicTitle(song.Title) {
			return candidate
		}
	}
	if comparableMusicTitle(title) == comparableMusicTitle(song.Title) {
		return title
	}
	return stripKnownArtistPrefix(song.Title, artists)
}

func stripKnownArtistPrefix(title string, artists []string) string {
	title = strings.TrimSpace(title)
	for _, artist := range uniqueMusicArtists(artists) {
		prefix := strings.TrimSpace(artist) + " - "
		if len(title) > len(prefix) && strings.EqualFold(title[:len(prefix)], prefix) {
			return strings.TrimSpace(title[len(prefix):])
		}
	}
	return title
}

func catalogSongArtistNames(song *model.Song) []string {
	artists := []string{}
	for _, credit := range song.ArtistCredits {
		if credit.Artist != nil {
			artists = append(artists, musicArtistSearchNames(*credit.Artist)...)
		}
	}
	for _, artist := range song.Artists {
		artists = append(artists, musicArtistSearchNames(artist)...)
	}
	if song.Album != nil {
		for _, credit := range song.Album.ArtistCredits {
			if credit.Artist != nil {
				artists = append(artists, musicArtistSearchNames(*credit.Artist)...)
			}
		}
		for _, artist := range song.Album.Artists {
			artists = append(artists, musicArtistSearchNames(artist)...)
		}
	}
	return uniqueMusicArtists(artists)
}

func FormatCatalogLyricsBackfillResult(result CatalogLyricsBackfillResult) string {
	status := "SKIP"
	if result.Matched {
		status = "MATCH"
	}
	if result.Applied {
		status = "APPLIED"
	}
	return fmt.Sprintf("%s\t%s\t%s => %s\tduration=%.0f\t%s", status, result.SongID, result.Artist, result.SongTitle, result.DurationSeconds, result.Reason)
}
