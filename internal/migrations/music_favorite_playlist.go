package migrations

import (
	"errors"
	"fmt"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func RunMusicFavoritePlaylistMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.User{}) || !db.Migrator().HasTable(&model.Playlist{}) || !db.Migrator().HasTable(&model.PlaylistSong{}) {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if tx.Migrator().HasColumn("music_playlists", "is_favorite") {
			if err := tx.Exec("UPDATE music_playlists SET kind = 'favorite' WHERE is_favorite = TRUE AND deleted_at IS NULL").Error; err != nil {
				return fmt.Errorf("restore legacy favorite playlist kind: %w", err)
			}
		}
		var users []model.User
		if err := tx.Find(&users).Error; err != nil {
			return fmt.Errorf("find users for favorite playlists: %w", err)
		}
		for _, user := range users {
			favorite, err := ensureFavoritePlaylist(tx, user.UUID)
			if err != nil {
				return err
			}
			if tx.Migrator().HasTable(&model.SongBookmark{}) {
				var bookmarks []model.SongBookmark
				if err := tx.Where("user_id = ?", user.UUID).Order("created_at ASC, id ASC").Find(&bookmarks).Error; err != nil {
					return fmt.Errorf("find song bookmarks: %w", err)
				}
				if err := appendFavoriteSongs(tx, favorite.ID, bookmarks); err != nil {
					return err
				}
			}
		}
		if tx.Migrator().HasTable(&model.SongBookmark{}) {
			if err := tx.Migrator().DropTable(&model.SongBookmark{}); err != nil {
				return fmt.Errorf("drop standalone song bookmarks table: %w", err)
			}
		}
		if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_music_playlists_user_favorite
			ON music_playlists (user_id)
			WHERE deleted_at IS NULL AND kind = 'favorite'`).Error; err != nil {
			return fmt.Errorf("create favorite playlist index: %w", err)
		}
		if tx.Migrator().HasColumn("music_playlists", "is_favorite") {
			if err := tx.Exec("ALTER TABLE music_playlists DROP COLUMN is_favorite").Error; err != nil {
				return fmt.Errorf("drop legacy favorite playlist column: %w", err)
			}
		}
		return nil
	})
}

func ensureFavoritePlaylist(tx *gorm.DB, userID uuid.UUID) (model.Playlist, error) {
	var favorites []model.Playlist
	if err := tx.Where("user_id = ? AND kind = ?", userID, "favorite").Order("created_at ASC, id ASC").Find(&favorites).Error; err != nil {
		return model.Playlist{}, fmt.Errorf("find favorite playlist: %w", err)
	}
	if len(favorites) == 0 {
		favorite := model.Playlist{UserID: userID, Name: "最爱", Kind: "favorite", IsPublic: false}
		if err := tx.Create(&favorite).Error; err != nil {
			return model.Playlist{}, fmt.Errorf("create favorite playlist: %w", err)
		}
		return favorite, nil
	}

	favorite := favorites[0]
	if err := tx.Model(&favorite).Updates(map[string]any{"name": "最爱", "is_public": false}).Error; err != nil {
		return model.Playlist{}, fmt.Errorf("normalize favorite playlist: %w", err)
	}
	favorite.Name = "最爱"
	favorite.IsPublic = false
	favoriteIDs := make([]uuid.UUID, 0, len(favorites))
	for _, playlist := range favorites {
		favoriteIDs = append(favoriteIDs, playlist.ID)
	}
	if err := tx.Unscoped().Where("playlist_id IN ?", favoriteIDs).Delete(&model.PlaylistBookmark{}).Error; err != nil {
		return model.Playlist{}, fmt.Errorf("delete favorite playlist bookmarks: %w", err)
	}
	for _, duplicate := range favorites[1:] {
		var songs []model.PlaylistSong
		if err := tx.Where("playlist_id = ?", duplicate.ID).Order("position ASC, created_at ASC").Find(&songs).Error; err != nil {
			return model.Playlist{}, fmt.Errorf("find duplicate favorite songs: %w", err)
		}
		bookmarks := make([]model.SongBookmark, 0, len(songs))
		for _, song := range songs {
			bookmarks = append(bookmarks, model.SongBookmark{Base: song.Base, UserID: userID, SongID: song.SongID})
		}
		if err := appendFavoriteSongs(tx, favorite.ID, bookmarks); err != nil {
			return model.Playlist{}, err
		}
		if err := tx.Unscoped().Where("playlist_id = ?", duplicate.ID).Delete(&model.PlaylistSong{}).Error; err != nil {
			return model.Playlist{}, fmt.Errorf("delete duplicate favorite songs: %w", err)
		}
		if err := tx.Unscoped().Delete(&model.Playlist{}, "id = ?", duplicate.ID).Error; err != nil {
			return model.Playlist{}, fmt.Errorf("delete duplicate favorite playlist: %w", err)
		}
	}
	return favorite, nil
}

func appendFavoriteSongs(tx *gorm.DB, playlistID uuid.UUID, bookmarks []model.SongBookmark) error {
	var maxPosition int
	if err := tx.Model(&model.PlaylistSong{}).Where("playlist_id = ?", playlistID).
		Select("COALESCE(MAX(position), 0)").Scan(&maxPosition).Error; err != nil {
		return fmt.Errorf("find favorite playlist position: %w", err)
	}
	for _, bookmark := range bookmarks {
		var existing model.PlaylistSong
		err := tx.Where("playlist_id = ? AND song_id = ?", playlistID, bookmark.SongID).First(&existing).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("find favorite playlist song: %w", err)
		}
		maxPosition++
		entry := model.PlaylistSong{
			Base:       model.Base{CreatedAt: bookmark.CreatedAt, UpdatedAt: bookmark.UpdatedAt},
			PlaylistID: playlistID,
			SongID:     bookmark.SongID,
			Position:   maxPosition,
		}
		if err := tx.Create(&entry).Error; err != nil {
			return fmt.Errorf("create favorite playlist song: %w", err)
		}
	}
	return nil
}
