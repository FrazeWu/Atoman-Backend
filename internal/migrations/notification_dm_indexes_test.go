package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"gorm.io/gorm"
)

func TestRunNotificationDMIndexesCreatesExpectedIndexes(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Notification{}, &model.DMConversation{}, &model.DMMessage{})

	if err := RunNotificationDMIndexes(db); err != nil {
		t.Fatalf("run notification/dm indexes migration: %v", err)
	}

	if !db.Migrator().HasTable(&model.DMConversation{}) {
		t.Fatal("expected dm_conversations table to exist")
	}
	if !db.Migrator().HasTable(&model.DMMessage{}) {
		t.Fatal("expected dm_messages table to exist")
	}

	assertIndexExists(t, db, "notifications", "uq_notification_dedup")
	assertIndexExists(t, db, "notifications", "idx_notification_recipient_read")
	assertIndexExists(t, db, "dm_conversations", "uq_dm_conversation")
	assertIndexExists(t, db, "dm_messages", "idx_dm_message_conv_created")
	assertIndexExists(t, db, "dm_messages", "idx_dm_message_conv_sender_read")
}

func assertIndexExists(t *testing.T, db *gorm.DB, table, name string) {
	t.Helper()
	if !db.Migrator().HasIndex(table, name) {
		t.Fatalf("expected index %s on %s to exist", name, table)
	}
}
