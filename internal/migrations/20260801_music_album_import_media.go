package migrations

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func RunMusicAlbumImportMediaMigration(db *gorm.DB) error {
	prefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
	if prefix == "" || !db.Migrator().HasTable(&model.AlbumImportSession{}) {
		return nil
	}

	var sessions []model.AlbumImportSession
	if err := db.Where("status = ? AND payload_json LIKE ?", "committed", "%cover_key%").Find(&sessions).Error; err != nil {
		return err
	}

	for _, session := range sessions {
		payload := map[string]any{}
		if err := json.Unmarshal([]byte(session.PayloadJSON), &payload); err != nil {
			continue
		}
		key, _ := payload["cover_key"].(string)
		key = strings.TrimLeft(strings.TrimSpace(key), "/")
		if key == "" {
			continue
		}

		var albumIDs []uuid.UUID
		pattern := "%/sessions/" + session.ID.String() + "/%"
		if err := db.Model(&model.Song{}).
			Distinct("album_id").
			Where("album_id IS NOT NULL AND audio_url LIKE ?", pattern).
			Pluck("album_id", &albumIDs).Error; err != nil {
			return err
		}
		if len(albumIDs) == 0 {
			continue
		}

		if err := db.Model(&model.Album{}).
			Where("id IN ? AND (cover_url = '' OR cover_url IS NULL)", albumIDs).
			Updates(map[string]any{
				"cover_url":    prefix + "/" + key,
				"cover_source": "s3",
			}).Error; err != nil {
			return err
		}
		if err := backfillAlbumImportArtistImage(db, session, payload, albumIDs); err != nil {
			return err
		}
	}
	return nil
}

func backfillAlbumImportArtistImage(db *gorm.DB, session model.AlbumImportSession, payload map[string]any, albumIDs []uuid.UUID) error {
	if session.UserID == nil || session.CreatedAt.IsZero() || !isSingleNewPersonImport(payload) || !db.Migrator().HasTable(&model.MediaAsset{}) {
		return nil
	}

	var artistIDs []uuid.UUID
	if err := db.Table(model.AlbumArtist{}.TableName()).
		Distinct("artist_id").
		Where("album_id IN ?", albumIDs).
		Pluck("artist_id", &artistIDs).Error; err != nil {
		return err
	}
	if len(artistIDs) != 1 {
		return nil
	}

	var assets []model.MediaAsset
	if err := db.Where(
		"user_id = ? AND purpose = ? AND created_at >= ? AND created_at <= ?",
		*session.UserID,
		"music.cover",
		session.CreatedAt.Add(-time.Hour),
		session.CreatedAt,
	).Find(&assets).Error; err != nil {
		return err
	}
	if len(assets) != 1 {
		return nil
	}

	return db.Model(&model.Artist{}).
		Where("id = ? AND (image_url = '' OR image_url IS NULL)", artistIDs[0]).
		Update("image_url", assets[0].URL).Error
}

func isSingleNewPersonImport(payload map[string]any) bool {
	request, ok := payload["commit_request"].(map[string]any)
	if !ok {
		return false
	}
	artists, ok := request["artists"].([]any)
	if !ok || len(artists) != 1 {
		return false
	}
	artist, ok := artists[0].(map[string]any)
	if !ok {
		return false
	}
	return strings.TrimSpace(stringValueFromMigration(artist["artist_id"])) == "" &&
		strings.EqualFold(strings.TrimSpace(stringValueFromMigration(artist["artist_form"])), "person") &&
		strings.TrimSpace(stringValueFromMigration(artist["image_url"])) == ""
}

func stringValueFromMigration(value any) string {
	result, _ := value.(string)
	return result
}
