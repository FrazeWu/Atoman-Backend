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

	if db.Migrator().HasTable("dm_conversations") {
		if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_dm_conversation ON dm_conversations (participant_a, participant_b)`).Error; err != nil {
			return err
		}
	}
	if db.Migrator().HasTable("dm_messages") {
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_dm_message_conv_created ON dm_messages (conversation_id, created_at)`).Error; err != nil {
			return err
		}
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_dm_message_conv_sender_read ON dm_messages (conversation_id, sender_id, read_at)`).Error; err != nil {
			return err
		}
	}

	return nil
}
