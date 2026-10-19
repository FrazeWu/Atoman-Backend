package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

// RunShortNoteMediaAttachmentMigration ensures short_notes table has media attachment columns and index.
func RunShortNoteMediaAttachmentMigration(db *gorm.DB) error {
	statements := []string{
		`ALTER TABLE short_notes ADD COLUMN IF NOT EXISTS media_attachment_type varchar(32);`,
		`ALTER TABLE short_notes ADD COLUMN IF NOT EXISTS media_attachment_id uuid;`,
		`CREATE INDEX IF NOT EXISTS idx_short_notes_media_attachment_id ON short_notes (media_attachment_id) WHERE deleted_at IS NULL AND media_attachment_id IS NOT NULL;`,
	}
	for _, stmt := range statements {
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("execute short note media attachment migration %q: %w", stmt, err)
		}
	}
	return nil
}
