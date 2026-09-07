package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunMusicMatchStateMigration backfills the first durable audio and match state.
// It is intentionally idempotent because migration steps run on every startup.
func RunMusicMatchStateMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.Song{}) {
		return nil
	}

	if err := db.Model(&model.Song{}).
		Where("audio_status IS NULL OR audio_status = '' OR audio_status = ?", "missing").
		Where("TRIM(COALESCE(audio_url, '')) <> ''").
		Updates(map[string]any{"audio_status": "ready"}).Error; err != nil {
		return err
	}
	return db.Exec(`CREATE INDEX IF NOT EXISTS idx_music_songs_audio_state
		ON "Songs" (audio_status, lifecycle_status, created_at)
		WHERE deleted_at IS NULL`).Error
}
