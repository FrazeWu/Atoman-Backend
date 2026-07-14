package main

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/google/uuid"
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
	if !db.Migrator().HasTable(&model.UserDefaultChannel{}) {
		t.Fatal("expected user_default_channels table to exist")
	}
	if !db.Migrator().HasTable(&model.CommentPublishRecord{}) {
		t.Fatal("expected comment_publish_records table to exist")
	}

	assertIndexExists(t, db, "notifications", "idx_notification_recipient_read")
	assertIndexExists(t, db, "dm_messages", "idx_dm_message_conv_sender_read")
}

func TestRunMigrationsDeduplicatesLegacyForumDrafts(t *testing.T) {
	db := testdb.Open(t)
	if err := db.Exec(`
CREATE TABLE forum_drafts (
	id TEXT PRIMARY KEY,
	created_at DATETIME,
	updated_at DATETIME,
	deleted_at DATETIME,
	user_id TEXT NOT NULL,
	context_key TEXT NOT NULL,
	title TEXT,
	content TEXT,
	tags TEXT
)`).Error; err != nil {
		t.Fatalf("create legacy forum_drafts table: %v", err)
	}

	userID := uuid.MustParse("99999999-9999-7999-8999-999999999999")
	contextKey := "reply:topic-5"
	olderID := uuid.MustParse("aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa")
	newerID := uuid.MustParse("bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb")
	olderTime := time.Date(2026, 6, 29, 8, 0, 0, 0, time.UTC)
	newerTime := olderTime.Add(time.Hour)

	if err := db.Exec(`
INSERT INTO forum_drafts (id, created_at, updated_at, user_id, context_key, title, content, tags)
VALUES (?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?)`,
		olderID, olderTime, olderTime, userID, contextKey, "old", "old body", "alpha",
		newerID, newerTime, newerTime, userID, contextKey, "new", "new body", "beta",
	).Error; err != nil {
		t.Fatalf("seed duplicate forum drafts: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	assertIndexExists(t, db, "forum_drafts", "idx_forum_drafts_user_context")

	var drafts []model.ForumDraft
	if err := db.Where("user_id = ? AND context_key = ?", userID, contextKey).Find(&drafts).Error; err != nil {
		t.Fatalf("query drafts: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("expected 1 forum draft, got %d", len(drafts))
	}
	if drafts[0].ID != newerID {
		t.Fatalf("expected newest draft %s to survive, got %s", newerID, drafts[0].ID)
	}
}

func assertIndexExists(t *testing.T, db *gorm.DB, table, name string) {
	t.Helper()
	if !db.Migrator().HasIndex(table, name) {
		t.Fatalf("expected index %s on %s to exist", name, table)
	}
}
