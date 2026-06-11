package main

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"gorm.io/gorm"
)

func TestMigrateSchemaCreatesDMTablesAndUnreadCountIndexes(t *testing.T) {
	db := testdb.Open(t)

	if err := migrateSchema(db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}

	if !db.Migrator().HasTable(&model.DMConversation{}) {
		t.Fatal("expected dm_conversations table to exist")
	}
	if !db.Migrator().HasTable(&model.DMMessage{}) {
		t.Fatal("expected dm_messages table to exist")
	}

	assertIndexExists(t, db, "notifications", "idx_notification_recipient_read")
	assertIndexExists(t, db, "dm_messages", "idx_dm_message_conv_sender_read")
}

func assertIndexExists(t *testing.T, db *gorm.DB, table, name string) {
	t.Helper()
	if !db.Migrator().HasIndex(table, name) {
		t.Fatalf("expected index %s on %s to exist", name, table)
	}
}
