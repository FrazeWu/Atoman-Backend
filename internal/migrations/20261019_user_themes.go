package migrations

import (
	"fmt"

	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunUserThemesMigration ensures the user_themes table and idx_user_themes_user_id index exist.
func RunUserThemesMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.UserTheme{}); err != nil {
		return fmt.Errorf("auto-migrate user_themes schema: %w", err)
	}

	if err := db.Exec(`
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_themes_user_id
ON user_themes (user_id) WHERE deleted_at IS NULL;`).Error; err != nil {
		return fmt.Errorf("ensure idx_user_themes_user_id index: %w", err)
	}

	return nil
}
