package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func RunForumReportUniqueIndex(db *gorm.DB) error {
	if !db.Migrator().HasTable("forum_reports") {
		return nil
	}
	if err := db.Exec(`
		DELETE FROM forum_reports
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY user_id, target_type, target_id
					ORDER BY created_at ASC, id ASC
				) AS row_number
				FROM forum_reports
			) ranked
			WHERE row_number > 1
		)
	`).Error; err != nil {
		return fmt.Errorf("deduplicate forum reports: %w", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_forum_reports_user_target ON forum_reports (user_id, target_type, target_id)`).Error; err != nil {
		return fmt.Errorf("create forum report unique index: %w", err)
	}
	return nil
}
