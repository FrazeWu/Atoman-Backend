package migrations

import (
	"fmt"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func RunMusicListeningMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.MusicListeningHistory{}, &model.PlaylistSong{}); err != nil {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var playlistIDs []uuid.UUID
		if err := tx.Model(&model.PlaylistSong{}).
			Where("position <= 0").
			Distinct("playlist_id").
			Pluck("playlist_id", &playlistIDs).Error; err != nil {
			return fmt.Errorf("find unordered playlists: %w", err)
		}
		for _, playlistID := range playlistIDs {
			var rows []model.PlaylistSong
			if err := tx.Where("playlist_id = ?", playlistID).
				Order("created_at ASC, id ASC").
				Find(&rows).Error; err != nil {
				return fmt.Errorf("find playlist songs: %w", err)
			}
			for index, row := range rows {
				if err := tx.Model(&model.PlaylistSong{}).Where("id = ?", row.ID).
					Update("position", index+1).Error; err != nil {
					return fmt.Errorf("backfill playlist song position: %w", err)
				}
			}
		}
		return nil
	})
}
