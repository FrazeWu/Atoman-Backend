package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunMusicPlayCountsMigration(db *gorm.DB) error {
	return db.AutoMigrate(&model.Song{})
}
