package migrations

import (
	"fmt"

	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunMusicCatalogV2Migration adds catalog metadata without changing existing entries.
func RunMusicCatalogV2Migration(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.Album{}, &model.Playlist{}, &model.AlbumArtist{}, &model.SongArtist{}); err != nil {
		return err
	}
	if !db.Migrator().HasTable(&model.Playlist{}) {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Playlist{}).Where("COALESCE(kind, '') = ''").Update("kind", "user").Error; err != nil {
			return fmt.Errorf("backfill playlist kind: %w", err)
		}
		if err := tx.Model(&model.AlbumArtist{}).Where("position = ?", 0).Update("position", 1).Error; err != nil {
			return fmt.Errorf("backfill album artist position: %w", err)
		}
		if err := tx.Model(&model.SongArtist{}).Where("position = ?", 0).Update("position", 1).Error; err != nil {
			return fmt.Errorf("backfill song artist position: %w", err)
		}
		return tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_music_playlists_user_system_kind
			ON music_playlists (user_id, kind)
			WHERE kind = 'later' AND deleted_at IS NULL`).Error
	})
}
