package main

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type legacyEmailVerificationCode struct {
	UUID      uuid.UUID `gorm:"type:uuid;primaryKey"`
	Email     string    `gorm:"uniqueIndex;not null"`
	Code      string    `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null"`
	Used      bool      `gorm:"default:false"`
	CreatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (legacyEmailVerificationCode) TableName() string { return "email_verification_codes" }

func TestMigrateSchemaCreatesDMV2(t *testing.T) {
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
	if !db.Migrator().HasTable(&model.DMImage{}) || !db.Migrator().HasTable(&model.DMChannelSettings{}) || !db.Migrator().HasTable(&model.DMMessageReport{}) {
		t.Fatal("expected dm v2 tables to exist")
	}
	if !db.Migrator().HasTable(&model.UserStudioState{}) {
		t.Fatal("expected user_studio_states table to exist")
	}
	if !db.Migrator().HasTable(&model.StudioModuleSettings{}) {
		t.Fatal("expected studio_module_settings table to exist")
	}
	if db.Migrator().HasTable("user_default_channels") {
		t.Fatal("did not expect legacy user_default_channels table")
	}
	if !db.Migrator().HasTable(&model.CommentPublishRecord{}) {
		t.Fatal("expected comment_publish_records table to exist")
	}
	if !db.Migrator().HasTable(&model.ForumUserModerationAction{}) {
		t.Fatal("expected forum_user_moderation_actions table to exist")
	}
	if !db.Migrator().HasTable(&model.ForumUserTrust{}) {
		t.Fatal("expected forum_user_trust table to exist")
	}
	if !db.Migrator().HasTable(&model.DebateRelation{}) {
		t.Fatal("expected debate_relations table to exist")
	}
	if !db.Migrator().HasTable(&model.DebateConclusionEvent{}) || !db.Migrator().HasTable(&model.DebateRevisionReference{}) {
		t.Fatal("expected debate wiki tables to exist")
	}

	assertIndexExists(t, db, "notifications", "idx_notification_recipient_read")
	assertIndexExists(t, db, "dm_messages", "idx_dm_message_conv_sender_read")
	assertIndexExists(t, db, "dm_conversations", "uq_dm_conversation_typed")
}

func TestMigrateSchemaCreatesOnboardingRecommendationTable(t *testing.T) {
	db := testdb.Open(t)
	if err := migrateSchema(db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	if !db.Migrator().HasTable(&model.OnboardingFeedRecommendation{}) {
		t.Fatal("expected onboarding feed recommendation table")
	}
}

func TestRunMigrationsAddsPasswordResetAuthSchema(t *testing.T) {
	db := testdb.Open(t)
	if err := db.AutoMigrate(&legacyEmailVerificationCode{}); err != nil {
		t.Fatalf("create legacy verification table: %v", err)
	}
	if err := db.Create(&legacyEmailVerificationCode{
		UUID: uuid.New(), Email: "legacy@example.com", Code: "123456",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed legacy verification code: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	if !db.Migrator().HasColumn(&model.User{}, "auth_version") {
		t.Fatal("expected users.auth_version column")
	}
	var legacy model.EmailVerificationCode
	if err := db.First(&legacy, "email = ?", "legacy@example.com").Error; err != nil {
		t.Fatalf("load legacy verification code: %v", err)
	}
	if legacy.Purpose != "registration" {
		t.Fatalf("expected legacy purpose registration, got %q", legacy.Purpose)
	}
	resetCode := model.EmailVerificationCode{
		Email:     legacy.Email,
		Purpose:   "password_reset",
		CodeHash:  "654321",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := db.Create(&resetCode).Error; err != nil {
		t.Fatalf("create password reset code beside registration code: %v", err)
	}
}

func TestRunMigrationsBackfillsLegacyForumReplies(t *testing.T) {
	db := testdb.Open(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get test database connection: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.User{}, &model.ForumCategory{}, &model.ForumTopic{}); err != nil {
		t.Fatalf("create forum topic schema: %v", err)
	}
	if err := db.Exec(`ALTER TABLE forum_topics ADD COLUMN solved_reply_id TEXT`).Error; err != nil {
		t.Fatalf("add legacy solved reply column: %v", err)
	}
	if err := migrateSchema(db); err != nil {
		t.Fatalf("migrate initial schema: %v", err)
	}
	if err := db.Exec("DISCARD PLANS").Error; err != nil {
		t.Fatalf("discard cached PostgreSQL plans: %v", err)
	}
	if err := db.Exec(`CREATE TABLE forum_replies (id TEXT PRIMARY KEY, created_at TIMESTAMP, updated_at TIMESTAMP, deleted_at TIMESTAMP, topic_id TEXT NOT NULL, user_id TEXT NOT NULL, parent_reply_id TEXT, content TEXT NOT NULL, floor_number INTEGER, is_solved NUMERIC)`).Error; err != nil {
		t.Fatalf("create legacy forum replies table: %v", err)
	}
	topicID, ownerID, categoryID, replyID, authorID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if err := db.Create(&model.User{
		UUID: ownerID, Username: "legacy-topic-owner", Email: "legacy-topic-owner@example.com", Password: "hash", IsActive: true,
	}).Error; err != nil {
		t.Fatalf("seed legacy topic owner: %v", err)
	}
	if err := db.Create(&model.ForumCategory{Base: model.Base{ID: categoryID}, Name: "legacy-category"}).Error; err != nil {
		t.Fatalf("seed legacy topic category: %v", err)
	}
	topic := model.ForumTopic{Base: model.Base{ID: topicID}, UserID: ownerID, CategoryID: categoryID, Title: "legacy", Content: "legacy"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("seed legacy topic: %v", err)
	}
	if err := db.Exec(`INSERT INTO forum_replies (id, topic_id, user_id, content, floor_number) VALUES (?, ?, ?, ?, ?)`, replyID, topicID, authorID, "legacy reply", 1).Error; err != nil {
		t.Fatalf("seed legacy reply: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	var entry model.CommentEntry
	if err := db.First(&entry, "id = ?", replyID).Error; err != nil {
		t.Fatalf("expected legacy reply to be migrated: %v", err)
	}
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
	var channels []model.Channel
	if err := db.Where("user_id = ?", user.UUID).Find(&channels).Error; err != nil {
		t.Fatalf("find studio channels: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("expected one studio channel, got %d", len(channels))
	}
	var state model.UserStudioState
	if err := db.First(&state, "user_id = ?", user.UUID).Error; err != nil {
		t.Fatalf("find studio state: %v", err)
	}
	if state.ChannelID == nil || *state.ChannelID != channels[0].ID {
		t.Fatalf("expected current channel %s, got %#v", channels[0].ID, state.ChannelID)
	}
	var collections int64
	if err := db.Model(&model.ContentCollection{}).
		Where("channel_id = ? AND is_default = ?", channels[0].ID, true).
		Count(&collections).Error; err != nil || collections != 1 {
		t.Fatalf("expected one default content collection, got %d err=%v", collections, err)
	}
	if db.Migrator().HasTable("user_default_channels") {
		t.Fatal("expected legacy default channel selections to be removed")
	}
	var playlists int64
	if err := db.Model(&model.Playlist{}).Where("user_id = ?", user.UUID).Count(&playlists).Error; err != nil || playlists != 0 {
		t.Fatalf("expected no implicit music playlists, got %d err=%v", playlists, err)
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

func TestRunMigrationsCreatesUnifiedStudioStateAndTypedCollections(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{})
	user := model.User{Username: "studio-user", Email: "studio-user@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	var state model.UserStudioState
	if err := db.First(&state, "user_id = ?", user.UUID).Error; err != nil {
		t.Fatalf("load studio state: %v", err)
	}
	if state.ChannelID == nil {
		t.Fatal("expected a current studio channel")
	}

	var collections int64
	if err := db.Model(&model.ContentCollection{}).
		Joins("JOIN channels ON channels.id = content_collections.channel_id").
		Where("channels.user_id = ? AND content_collections.is_default = ?", user.UUID, true).
		Count(&collections).Error; err != nil {
		t.Fatalf("count default content collections: %v", err)
	}
	if collections != 1 {
		t.Fatalf("expected one default content collection, got %d", collections)
	}
}

func TestRunMigrationsDeduplicatesLegacyForumDrafts(t *testing.T) {
	db := testdb.Open(t)
	if err := db.Exec(`
CREATE TABLE forum_drafts (
	id TEXT PRIMARY KEY,
	created_at TIMESTAMP,
	updated_at TIMESTAMP,
	deleted_at TIMESTAMP,
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

func TestMigrateSchemaCreatesForumGroupPermissionTables(t *testing.T) {
	db := testdb.Open(t)
	if err := migrateSchema(db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	for _, table := range []any{
		&model.ForumGroup{},
		&model.ForumGroupMember{},
		&model.ForumCategoryPermission{},
	} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected table for %T", table)
		}
	}
}

func assertIndexExists(t *testing.T, db *gorm.DB, table, name string) {
	t.Helper()
	if !db.Migrator().HasIndex(table, name) {
		t.Fatalf("expected index %s on %s to exist", name, table)
	}
}
