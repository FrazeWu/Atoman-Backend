package migrations

import (
	"fmt"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func RunMusicFavoritePlaylistMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.Playlist{}) || !db.Migrator().HasTable(&model.SongBookmark{}) {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var favorites []model.Playlist
		query := tx.Unscoped().Where("kind = ?", "favorite")
		if tx.Migrator().HasColumn("music_playlists", "is_favorite") {
			query = tx.Unscoped().Where("kind = ? OR is_favorite = ?", "favorite", true)
		}
		if err := query.Find(&favorites).Error; err != nil {
			return fmt.Errorf("find legacy favorite playlists: %w", err)
		}
		for _, playlist := range favorites {
			var songIDs []uuid.UUID
			if err := tx.Unscoped().Model(&model.PlaylistSong{}).Where("playlist_id = ?", playlist.ID).Pluck("song_id", &songIDs).Error; err != nil {
				return fmt.Errorf("find legacy favorite songs: %w", err)
			}
			for _, songID := range songIDs {
				bookmark := model.SongBookmark{UserID: playlist.UserID, SongID: songID}
				if err := tx.Where("user_id = ? AND song_id = ?", playlist.UserID, songID).
					FirstOrCreate(&bookmark).Error; err != nil {
					return fmt.Errorf("migrate favorite song bookmark: %w", err)
				}
			}
			if err := tx.Unscoped().Where("playlist_id = ?", playlist.ID).Delete(&model.PlaylistBookmark{}).Error; err != nil {
				return fmt.Errorf("delete legacy favorite playlist bookmarks: %w", err)
			}
			if err := tx.Unscoped().Where("playlist_id = ?", playlist.ID).Delete(&model.PlaylistSong{}).Error; err != nil {
				return fmt.Errorf("delete legacy favorite playlist songs: %w", err)
			}
			if err := tx.Unscoped().Delete(&model.Playlist{}, "id = ?", playlist.ID).Error; err != nil {
				return fmt.Errorf("delete legacy favorite playlist: %w", err)
			}
		}
		if tx.Migrator().HasIndex("music_playlists", "idx_music_playlists_user_favorite") {
			if err := tx.Migrator().DropIndex("music_playlists", "idx_music_playlists_user_favorite"); err != nil {
				return fmt.Errorf("drop legacy favorite playlist index: %w", err)
			}
		}
		if tx.Migrator().HasColumn("music_playlists", "is_favorite") {
			if err := tx.Exec("ALTER TABLE music_playlists DROP COLUMN is_favorite").Error; err != nil {
				return fmt.Errorf("drop legacy favorite playlist column: %w", err)
			}
		}
		return nil
	})
}
