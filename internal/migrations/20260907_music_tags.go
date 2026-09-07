package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunMusicTagsMigration(db *gorm.DB) error {
	return db.AutoMigrate(
		&model.MusicTag{},
		&model.MusicTagAssignment{},
		&model.MusicTagVote{},
	)
}
