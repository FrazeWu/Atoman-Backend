package migrations

import (
	"fmt"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func RunMusicFavoritePlaylistMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.Playlist{}) {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var candidates []model.Playlist
		if err := tx.Where("name = ? OR is_favorite = ?", "最爱", true).
			Order("user_id ASC, is_favorite DESC, created_at ASC, id ASC").
			Find(&candidates).Error; err != nil {
			return fmt.Errorf("find favorite playlists: %w", err)
		}
		canonicalByUser := make(map[uuid.UUID]uuid.UUID, len(candidates))
		if len(candidates) > 0 {
			candidateIDs := make([]uuid.UUID, 0, len(candidates))
			for _, playlist := range candidates {
				candidateIDs = append(candidateIDs, playlist.ID)
				if _, ok := canonicalByUser[playlist.UserID]; !ok {
					canonicalByUser[playlist.UserID] = playlist.ID
				}
			}
			if err := tx.Model(&model.Playlist{}).Where("id IN ?", candidateIDs).Update("is_favorite", false).Error; err != nil {
				return fmt.Errorf("reset favorite playlists: %w", err)
			}
			for _, playlistID := range canonicalByUser {
				if err := tx.Model(&model.Playlist{}).Where("id = ?", playlistID).
					Updates(map[string]any{"is_favorite": true, "is_public": false}).Error; err != nil {
					return fmt.Errorf("mark favorite playlist: %w", err)
				}
			}
		}
		var userIDs []uuid.UUID
		if err := tx.Model(&model.User{}).Pluck("uuid", &userIDs).Error; err != nil {
			return fmt.Errorf("find users for favorite playlists: %w", err)
		}
		for _, userID := range userIDs {
			if _, ok := canonicalByUser[userID]; ok {
				continue
			}
			playlist := model.Playlist{UserID: userID, Name: "最爱", IsFavorite: true}
			if err := tx.Create(&playlist).Error; err != nil {
				return fmt.Errorf("create favorite playlist: %w", err)
			}
			canonicalByUser[userID] = playlist.ID
		}
		if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_music_playlists_user_favorite
			ON music_playlists (user_id)
			WHERE is_favorite = TRUE AND deleted_at IS NULL`).Error; err != nil {
			return fmt.Errorf("create favorite playlist unique index: %w", err)
		}
		return nil
	})
}
