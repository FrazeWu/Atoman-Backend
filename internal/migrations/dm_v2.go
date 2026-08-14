package migrations

import (
	"fmt"

	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunDMV2Migration(db *gorm.DB) error {
	if err := addNullableDMV2Columns(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(&model.DMImage{}, &model.DMChannelSettings{}, &model.DMMessageReport{}); err != nil {
		return err
	}
	if db.Migrator().HasTable("dm_conversations") {
		if err := db.Exec(`UPDATE dm_conversations SET participant_a_type = 'user', participant_b_type = 'user' WHERE participant_a_type IS NULL OR participant_a_type = '' OR participant_b_type IS NULL OR participant_b_type = ''`).Error; err != nil {
			return err
		}
	}
	if db.Migrator().HasTable("dm_messages") {
		for _, statement := range []string{
			`UPDATE dm_messages SET sender_type = 'user' WHERE sender_type IS NULL OR sender_type = ''`,
			`UPDATE dm_messages SET actor_user_id = sender_id WHERE actor_user_id IS NULL OR actor_user_id = '00000000-0000-0000-0000-000000000000'`,
			`UPDATE dm_messages SET client_message_id = id WHERE client_message_id IS NULL OR client_message_id = '00000000-0000-0000-0000-000000000000'`,
		} {
			if err := db.Exec(statement).Error; err != nil {
				return err
			}
		}
	}
	if db.Migrator().HasTable("user_settings") {
		if err := db.Exec(`UPDATE user_settings SET dm_permission = 'one_before_reply' WHERE dm_permission IS NULL OR trim(dm_permission) = ''`).Error; err != nil {
			return err
		}
	}
	return createDMV2Constraints(db)
}

func addNullableDMV2Columns(db *gorm.DB) error {
	if !db.Migrator().HasTable("dm_conversations") {
		if err := db.AutoMigrate(&model.DMConversation{}); err != nil {
			return err
		}
	} else {
		for _, column := range []struct{ name, definition string }{
			{"participant_a_type", "varchar(16)"},
			{"participant_b_type", "varchar(16)"},
		} {
			if !db.Migrator().HasColumn("dm_conversations", column.name) {
				if err := db.Exec(fmt.Sprintf("ALTER TABLE dm_conversations ADD COLUMN %s %s", column.name, column.definition)).Error; err != nil {
					return err
				}
			}
		}
	}
	if !db.Migrator().HasTable("dm_messages") {
		return db.AutoMigrate(&model.DMMessage{})
	}
	for _, column := range []struct{ name, definition string }{
		{"sender_type", "varchar(16)"},
		{"actor_user_id", "uuid"},
		{"client_message_id", "uuid"},
		{"image_id", "uuid"},
	} {
		if !db.Migrator().HasColumn("dm_messages", column.name) {
			if err := db.Exec(fmt.Sprintf("ALTER TABLE dm_messages ADD COLUMN %s %s", column.name, column.definition)).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func createDMV2Constraints(db *gorm.DB) error {
	statements := []string{
		`DROP INDEX IF EXISTS uq_dm_conversation`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_dm_conversation_typed ON dm_conversations (participant_a_type, participant_a, participant_b_type, participant_b) WHERE deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_dm_actor_client_message ON dm_messages (actor_user_id, client_message_id) WHERE deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_dm_message_image ON dm_messages (image_id) WHERE image_id IS NOT NULL AND deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_dm_message_reporter ON dm_message_reports (message_id, reporter_user_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_dm_message_conv_created ON dm_messages (conversation_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_dm_message_conv_sender_read ON dm_messages (conversation_id, sender_id, read_at)`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	for _, statement := range []string{
		`ALTER TABLE dm_messages DROP CONSTRAINT IF EXISTS fk_dm_messages_sender`,
		`ALTER TABLE dm_conversations ALTER COLUMN participant_a_type SET NOT NULL`,
		`ALTER TABLE dm_conversations ALTER COLUMN participant_b_type SET NOT NULL`,
		`ALTER TABLE dm_messages ALTER COLUMN sender_type SET NOT NULL`,
		`ALTER TABLE dm_messages ALTER COLUMN actor_user_id SET NOT NULL`,
		`ALTER TABLE dm_messages ALTER COLUMN client_message_id SET NOT NULL`,
		`ALTER TABLE dm_conversations ALTER COLUMN participant_a_type SET DEFAULT 'user'`,
		`ALTER TABLE dm_conversations ALTER COLUMN participant_b_type SET DEFAULT 'user'`,
		`ALTER TABLE dm_messages ALTER COLUMN sender_type SET DEFAULT 'user'`,
		`ALTER TABLE dm_conversations DROP CONSTRAINT IF EXISTS chk_dm_conversation_a_user`,
		`ALTER TABLE dm_conversations DROP CONSTRAINT IF EXISTS chk_dm_conversation_b_type`,
		`ALTER TABLE dm_conversations DROP CONSTRAINT IF EXISTS chk_dm_conversation_user_order`,
		`ALTER TABLE dm_message_reports DROP CONSTRAINT IF EXISTS chk_dm_message_report_status`,
		`ALTER TABLE dm_conversations ADD CONSTRAINT chk_dm_conversation_a_user CHECK (participant_a_type = 'user')`,
		`ALTER TABLE dm_conversations ADD CONSTRAINT chk_dm_conversation_b_type CHECK (participant_b_type IN ('user', 'channel'))`,
		`ALTER TABLE dm_conversations ADD CONSTRAINT chk_dm_conversation_user_order CHECK (participant_b_type = 'channel' OR (participant_a <> participant_b AND participant_a::text < participant_b::text))`,
		`ALTER TABLE dm_message_reports ADD CONSTRAINT chk_dm_message_report_status CHECK (status IN ('pending', 'resolved', 'dismissed'))`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
