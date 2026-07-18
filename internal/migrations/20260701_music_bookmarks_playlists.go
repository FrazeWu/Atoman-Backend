package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunMusicBookmarksPlaylistsMigration(db *gorm.DB) error {
	return db.AutoMigrate(
		&model.ArtistBookmark{},
		&model.AlbumBookmark{},
		&model.SongBookmark{},
		&model.Playlist{},
		&model.PlaylistBookmark{},
		&model.PlaylistSong{},
	)
}
