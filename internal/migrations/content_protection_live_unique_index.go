package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func RunContentProtectionLiveUniqueIndex(db *gorm.DB) error {
	if !db.Migrator().HasTable("content_protections") {
		return nil
	}

	if err := db.Exec(`DROP INDEX IF EXISTS idx_content_protections_content_id`).Error; err != nil {
		return fmt.Errorf("drop legacy content protection unique index: %w", err)
	}
	if err := db.Exec(`DROP INDEX IF EXISTS idx_content_protections_live_content`).Error; err != nil {
		return fmt.Errorf("drop existing content protection live index: %w", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_content_protections_live_content
		ON content_protections (content_type, content_id)
		WHERE deleted_at IS NULL`).Error; err != nil {
		return fmt.Errorf("create content protection live unique index: %w", err)
	}

	return nil
}
