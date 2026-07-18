package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunUserBlocksMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.UserBlock{}); err != nil {
		return err
	}
	return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_user_block_pair ON user_blocks (blocker_id, blocked_id)`).Error
}
