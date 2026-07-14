package migrations

import "gorm.io/gorm"

func RunUnifiedCommentIndexes(db *gorm.DB) error {
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_discussion_target_kind_key ON discussion_targets (kind, resource_key)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_comment_root_floor ON comment_entries (target_id, floor_number) WHERE floor_number IS NOT NULL AND deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_comment_like_user ON comment_likes (comment_id, user_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_comment_report_user ON comment_reports (comment_id, reporter_id)`,
	}

	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
