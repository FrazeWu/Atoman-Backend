package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunMusicAlbumImportsMigration(db *gorm.DB) error {
	return db.AutoMigrate(&model.AlbumImportSession{})
}
