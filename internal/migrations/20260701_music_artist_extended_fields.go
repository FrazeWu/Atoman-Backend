package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunMusicArtistExtendedFieldsMigration(db *gorm.DB) error {
	return db.AutoMigrate(&model.Artist{}, &model.Album{})
}
