package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunDiscussionReadStateMigration(db *gorm.DB) error {
	return db.AutoMigrate(&model.DiscussionReadState{})
}
