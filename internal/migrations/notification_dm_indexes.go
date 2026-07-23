package migrations

import "gorm.io/gorm"

func RunNotificationDMIndexes(db *gorm.DB) error {
	if db.Migrator().HasTable("notifications") {
		statements := []string{
			`DROP INDEX IF EXISTS uq_notification_dedup`,
			`CREATE UNIQUE INDEX IF NOT EXISTS uq_notification_dedup ON notifications (recipient_id, source_type, source_id) WHERE aggregation_key = '' AND deleted_at IS NULL`,
			`CREATE UNIQUE INDEX IF NOT EXISTS uq_notification_unread_aggregate ON notifications (recipient_id, aggregation_key) WHERE aggregation_key <> '' AND read_at IS NULL AND deleted_at IS NULL`,
			`CREATE INDEX IF NOT EXISTS idx_notification_recipient_read ON notifications (recipient_id, read_at)`,
		}
		for _, statement := range statements {
			if err := db.Exec(statement).Error; err != nil {
				return err
			}
		}
	}

	return nil
}
