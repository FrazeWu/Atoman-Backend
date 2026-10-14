package migrations

import (
	"fmt"

	"atoman/internal/model"

	"gorm.io/gorm"
)

// MigrateUserBookAnnotations creates the user_book_annotations table and indexes.
func MigrateUserBookAnnotations(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.UserBookAnnotation{}); err != nil {
		return fmt.Errorf("migrate user book annotations schema: %w", err)
	}

	if err := db.Exec(`
CREATE INDEX IF NOT EXISTS idx_user_book_annotations_user_asset
ON user_book_annotations (user_id, asset_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_user_book_annotations_user_created
ON user_book_annotations (user_id, created_at DESC) WHERE deleted_at IS NULL;`).Error; err != nil {
		return fmt.Errorf("ensure user book annotations indexes: %w", err)
	}

	return nil
}
