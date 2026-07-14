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
	if !db.Migrator().HasTable(&model.UserSettings{}) {
		t.Fatal("expected user_settings table to exist")
	}
	if !db.Migrator().HasTable(&model.EmailVerificationCode{}) {
		t.Fatal("expected email_verification_codes table to exist")
	}

	assertIndexExists(t, db, "notifications", "idx_notification_recipient_read")
	assertIndexExists(t, db, "dm_messages", "idx_dm_message_conv_sender_read")
}

func TestRunMigrationsBackfillsUserDefaultResources(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{})
	user := model.User{Username: "legacy-user", Email: "legacy-user@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create legacy user: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	var settings int64
	if err := db.Model(&model.UserSettings{}).Where("user_id = ?", user.UUID).Count(&settings).Error; err != nil || settings != 1 {
		t.Fatalf("expected one user settings row, got %d err=%v", settings, err)
	}
	var selections []model.UserDefaultChannel
	if err := db.Preload("Channel").Where("user_id = ?", user.UUID).Find(&selections).Error; err != nil {
		t.Fatalf("find default channel selections: %v", err)
	}
	if len(selections) != 3 {
		t.Fatalf("expected three default channel selections, got %d", len(selections))
	}
	for _, selection := range selections {
		if selection.Channel == nil || selection.Channel.ContentType != selection.ContentType {
			t.Fatalf("unexpected default channel selection: %#v", selection)
		}
		var collections int64
		if err := db.Model(&model.Collection{}).Where("channel_id = ? AND is_default = ?", selection.ChannelID, true).Count(&collections).Error; err != nil || collections != 1 {
			t.Fatalf("expected one %s default collection, got %d err=%v", selection.ContentType, collections, err)
		}
	}
	var favorites int64
	if err := db.Model(&model.Playlist{}).Where("user_id = ? AND is_favorite = ?", user.UUID, true).Count(&favorites).Error; err != nil || favorites != 1 {
		t.Fatalf("expected one favorite playlist, got %d err=%v", favorites, err)
	}
	var folders int64
	if err := db.Model(&model.BookmarkFolder{}).Where("user_id = ? AND name = ?", user.UUID, "默认收藏夹").Count(&folders).Error; err != nil || folders != 1 {
		t.Fatalf("expected one default bookmark folder, got %d err=%v", folders, err)
	}
	var groups int64
	if err := db.Model(&model.SubscriptionGroup{}).Where("user_id = ? AND name = ?", user.UUID, "默认分组").Count(&groups).Error; err != nil || groups != 1 {
		t.Fatalf("expected one default subscription group, got %d err=%v", groups, err)
	}
	var subscriptions int64
	if err := db.Model(&model.Subscription{}).Where("user_id = ?", user.UUID).Count(&subscriptions).Error; err != nil || subscriptions != 1 {
		t.Fatalf("expected one self subscription, got %d err=%v", subscriptions, err)
	}
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

func TestMigrateSchemaCreatesForumFollows(t *testing.T) {
	db := testdb.Open(t)
	if err := migrateSchema(db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	if !db.Migrator().HasTable(&model.ForumFollow{}) {
		t.Fatal("expected forum_follows table")
	}
}

func assertIndexExists(t *testing.T, db *gorm.DB, table, name string) {
	t.Helper()
	if !db.Migrator().HasIndex(table, name) {
		t.Fatalf("expected index %s on %s to exist", name, table)
	}
}
