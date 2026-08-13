package migrations

import "gorm.io/gorm"

func RunUnifiedCommentIndexes(db *gorm.DB) error {
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_discussion_target_kind_key ON discussion_targets (kind, resource_key)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_comment_root_floor ON comment_entries (target_id, floor_number) WHERE floor_number IS NOT NULL AND deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_comment_like_user ON comment_likes (comment_id, user_id) WHERE deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_comment_report_user ON comment_reports (comment_id, reporter_id)`,
		`CREATE INDEX IF NOT EXISTS idx_comment_entries_target_status_floor ON comment_entries (target_id, status, floor_number) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_comment_entries_target_status_hot_floor ON comment_entries (target_id, status, hot_score, floor_number) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_comment_entries_root_status_created ON comment_entries (root_id, status, created_at, id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_comment_publish_author_created ON comment_publish_records (author_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_comment_publish_duplicate_window ON comment_publish_records (author_id, target_id, content_hash, created_at)`,
	}

	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
