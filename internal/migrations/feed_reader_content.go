package migrations

import "gorm.io/gorm"

// RunFeedReaderContentMigration marks existing successful extractions as page
// candidates and adds queue/quality indexes after AutoMigrate creates columns.
func RunFeedReaderContentMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable("feed_items") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE feed_items
			SET reader_html = full_text_html,
				reader_source = 'page',
				reader_quality_flags = '["legacy_unscored"]',
				reader_version = 1
			WHERE full_text_status = 'success'
				AND COALESCE(full_text_html, '') <> ''
				AND reader_version = 0`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_feed_items_reader_quality
			ON feed_items (reader_source, reader_quality_score)
			WHERE deleted_at IS NULL`).Error; err != nil {
			return err
		}
		return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_feed_items_fulltext_queue_age
			ON feed_items (full_text_status, next_full_text_attempt_at, created_at)
			WHERE deleted_at IS NULL`).Error
	})
}
