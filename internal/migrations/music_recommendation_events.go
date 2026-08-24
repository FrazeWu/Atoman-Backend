package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunMusicRecommendationEventsMigration(db *gorm.DB) error {
	return db.AutoMigrate(&model.MusicRecommendationEvent{})
}
