package migrations

import (
	"errors"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
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
	assertIndexExists(t, db, "notifications", "uq_notification_unread_aggregate")
	assertIndexExists(t, db, "notifications", "idx_notification_recipient_read")
	assertIndexExists(t, db, "dm_conversations", "uq_dm_conversation")
	assertIndexExists(t, db, "dm_messages", "idx_dm_message_conv_created")
	assertIndexExists(t, db, "dm_messages", "idx_dm_message_conv_sender_read")
}

func TestRunNotificationDMIndexesAllowsEmptyDatabase(t *testing.T) {
	db := testdb.Open(t)
	if err := RunNotificationDMIndexes(db); err != nil {
		t.Fatalf("empty database migration: %v", err)
	}
}

func TestRunNotificationDMIndexesSupportsImmediateAndUnreadAggregateSemantics(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Notification{}, &model.DMConversation{}, &model.DMMessage{})
	if err := RunNotificationDMIndexes(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := RunNotificationDMIndexes(db); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}

	recipient, source := uuid.New(), uuid.New()
	immediate := model.Notification{RecipientID: recipient, Type: "comment_reply", SourceType: "comment", SourceID: source}
	if err := db.Create(&immediate).Error; err != nil {
		t.Fatalf("create immediate: %v", err)
	}
	duplicate := model.Notification{RecipientID: recipient, Type: "comment_mention", SourceType: "comment", SourceID: source}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("expected immediate event deduplication")
	}

	aggregationKey := "comment_like:" + source.String()
	first := model.Notification{RecipientID: recipient, Type: "comment_like", SourceType: "comment", SourceID: source, AggregationKey: aggregationKey}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create aggregate: %v", err)
	}
	second := model.Notification{RecipientID: recipient, Type: "comment_like", SourceType: "comment", SourceID: source, AggregationKey: aggregationKey}
	if err := db.Create(&second).Error; err == nil {
		t.Fatal("expected only one unread aggregate")
	}
	now := time.Now()
	if err := db.Model(&first).Update("read_at", now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("expected a new aggregate after read: %v", err)
	}
	if errors.Is(db.Error, gorm.ErrRecordNotFound) {
		t.Fatal("unexpected record not found")
	}
}

func assertIndexExists(t *testing.T, db *gorm.DB, table, name string) {
	t.Helper()
	if !db.Migrator().HasIndex(table, name) {
		t.Fatalf("expected index %s on %s to exist", name, table)
	}
}
