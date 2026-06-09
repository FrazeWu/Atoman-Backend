package migrations

import "gorm.io/gorm"

func RunNotificationDMIndexes(db *gorm.DB) error {
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_notification_dedup
		ON notifications (recipient_id, source_type, source_id)`).Error; err != nil {
		return err
	}

	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_notification_recipient_read
		ON notifications (recipient_id, read_at)`).Error; err != nil {
		return err
	}

	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_dm_conversation
		ON dm_conversations (participant_a, participant_b)`).Error; err != nil {
		return err
	}

	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_dm_message_conv_created
		ON dm_messages (conversation_id, created_at)`).Error; err != nil {
		return err
	}

	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_dm_message_conv_sender_read
		ON dm_messages (conversation_id, sender_id, read_at)`).Error; err != nil {
		return err
	}

	return nil
}
