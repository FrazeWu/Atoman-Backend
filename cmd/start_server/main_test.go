package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestWaitForWorkersWaitsForEveryWorker(t *testing.T) {
	firstDone := make(chan struct{})
	secondDone := make(chan struct{})
	result := make(chan error, 1)

	go func() {
		result <- waitForWorkers(time.Second, firstDone, secondDone)
	}()

	close(firstDone)
	select {
	case err := <-result:
		t.Fatalf("waitForWorkers() returned before every worker stopped: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(secondDone)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("waitForWorkers() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waitForWorkers() did not return after every worker stopped")
	}
}

func TestWaitForWorkersTimesOutWhenWorkerDoesNotStop(t *testing.T) {
	blocked := make(chan struct{})

	err := waitForWorkers(20*time.Millisecond, blocked)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitForWorkers() error = %v, want context deadline exceeded", err)
	}
}

type legacyStartupEmailVerificationCode struct {
	UUID      uuid.UUID `gorm:"type:uuid;primaryKey"`
	Email     string    `gorm:"uniqueIndex;not null"`
	Code      string    `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null"`
	Used      bool      `gorm:"default:false"`
	CreatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (legacyStartupEmailVerificationCode) TableName() string { return "email_verification_codes" }

func TestCORSRejectsUnknownOriginWithCredentialsOutsideProduction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "development")
	t.Setenv("ALLOWED_ORIGINS", "")

	router := gin.New()
	router.Use(corsMiddleware(configuredAllowedOrigins()))
	router.GET("/ping", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected unknown origin to be rejected, got ACAO %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("expected credentials header to be absent for unknown origin, got %q", got)
	}
}

func TestCORSPreflightAllowsCSRFHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(corsMiddleware([]string{"https://www.atoman.org"}))

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/users/me/password", nil)
	req.Header.Set("Origin", "https://www.atoman.org")
	req.Header.Set("Access-Control-Request-Headers", "X-CSRF-Token")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected preflight 204, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Header().Get("Access-Control-Allow-Headers"), "X-CSRF-Token") {
		t.Fatalf("expected X-CSRF-Token in allowed headers, got %q", recorder.Header().Get("Access-Control-Allow-Headers"))
	}
	if got := recorder.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Fatalf("expected preflight max age 600, got %q", got)
	}
}

func TestValidateAuthEnvironmentRequiresCodeSecretOnlyInProduction(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("AUTH_CODE_SECRET", "")
	if err := validateAuthEnvironment(); err != nil {
		t.Fatalf("development auth environment should be valid: %v", err)
	}

	t.Setenv("ENV", "production")
	if err := validateAuthEnvironment(); err == nil || !strings.Contains(err.Error(), "AUTH_CODE_SECRET") {
		t.Fatalf("expected production AUTH_CODE_SECRET error, got %v", err)
	}

	t.Setenv("AUTH_CODE_SECRET", "production-auth-code-secret")
	if err := validateAuthEnvironment(); err != nil {
		t.Fatalf("production auth environment should be valid: %v", err)
	}
}

func TestRunUnifiedCommentStartupMigrationsCreatesTablesAndIndexes(t *testing.T) {
	db := testdb.Open(t)

	if err := runUnifiedCommentStartupMigrations(db); err != nil {
		t.Fatalf("run unified comment startup migrations: %v", err)
	}
	for _, table := range []string{
		"music_song_lyrics",
		"music_song_lyric_lines",
		"music_song_lyric_versions",
		"music_lyric_annotations",
		"music_lyric_annotation_votes",
	} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected startup migration to create %s", table)
		}
	}

	models := []any{
		&model.AuthSession{},
		&model.ExternalIdentity{},
		&model.OAuthFlow{},
		&model.ForumGroup{},
		&model.ForumGroupMember{},
		&model.ForumCategoryPermission{},
		&model.ForumUserModerationAction{},
		&model.ForumUserTrust{},
		&model.DiscussionTarget{},
		&model.CommentEntry{},
		&model.CommentMention{},
		&model.CommentAttachment{},
		&model.CommentLike{},
		&model.CommentReport{},
		&model.CommentTimeAnchor{},
		&model.CommentPublishRecord{},
		&model.TimelineRevisionProposal{},
		&model.Debate{},
		&model.DebateConclusionEvent{},
		&model.DebateRevisionReference{},
		&model.DebateVote{},
		&model.DebateRelation{},
	}
	for _, schemaModel := range models {
		if !db.Migrator().HasTable(schemaModel) {
			t.Fatalf("expected table for %T to exist", schemaModel)
		}
	}
	if !db.Migrator().HasColumn(&model.DiscussionTarget{}, "resource_id") {
		t.Fatal("expected discussion_targets.resource_id")
	}
	if !db.Migrator().HasColumn(&model.CommentEntry{}, "reply_to_author_id") {
		t.Fatal("expected comment_entries.reply_to_author_id")
	}

	for table, index := range map[string]string{
		"discussion_targets":      "uq_discussion_target_kind_key",
		"comment_entries":         "uq_comment_root_floor",
		"comment_likes":           "uq_comment_like_user",
		"comment_reports":         "uq_comment_report_user",
		"comment_publish_records": "idx_comment_publish_author_created",
	} {
		if !db.Migrator().HasIndex(table, index) {
			t.Fatalf("expected index %s on %s to exist", index, table)
		}
	}
}

func TestStartupMigrationsUpgradePasswordResetAuthSchema(t *testing.T) {
	db := testdb.Open(t)
	if err := db.AutoMigrate(&legacyStartupEmailVerificationCode{}); err != nil {
		t.Fatalf("create legacy verification schema: %v", err)
	}
	legacy := legacyStartupEmailVerificationCode{
		UUID: uuid.New(), Email: "legacy@example.com", Code: "123456",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy verification code: %v", err)
	}

	if err := runUnifiedCommentStartupMigrations(db, &model.User{}, &model.EmailVerificationCode{}); err != nil {
		t.Fatalf("run startup migrations: %v", err)
	}
	resetCode := model.EmailVerificationCode{
		Email: "legacy@example.com", Purpose: "password_reset", CodeHash: "654321",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := db.Create(&resetCode).Error; err != nil {
		t.Fatalf("create purpose-specific reset code after startup migration: %v", err)
	}
}

func TestRunMusicBookmarkStartupMigrationCreatesPlaylistBookmarksOnFreshDatabase(t *testing.T) {
	db := testdb.Open(t)

	if err := runMusicBookmarkStartupMigration(db); err != nil {
		t.Fatalf("run music bookmark startup migration: %v", err)
	}
	if !db.Migrator().HasTable(&model.PlaylistBookmark{}) {
		t.Fatal("expected startup migration to create music_playlist_bookmarks")
	}
	if !db.Migrator().HasIndex(&model.PlaylistBookmark{}, "idx_music_playlist_bookmarks_user_playlist") {
		t.Fatal("expected startup migration to create playlist bookmark unique index")
	}
}

func TestStartupDMV2MigrationOrder(t *testing.T) {
	db := testdb.Open(t)
	if err := runStartupDMV2Migration(db); err != nil {
		t.Fatalf("run startup dm v2 migration: %v", err)
	}
	if !db.Migrator().HasTable(&model.DMConversation{}) || !db.Migrator().HasTable(&model.DMMessage{}) {
		t.Fatal("expected startup migration to create dm v2 core tables")
	}
	if !db.Migrator().HasIndex("dm_conversations", "uq_dm_conversation_typed") {
		t.Fatal("expected startup migration to create typed conversation index")
	}
}

func TestRunUnifiedCommentStartupMigrationsBackfillsLegacyForumReplies(t *testing.T) {
	db := testdb.Open(t)
	requireLegacyForumTables(t, db)
	topicID, ownerID, replyID, authorID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if err := db.Exec(`INSERT INTO forum_topics (id, user_id) VALUES (?, ?)`, topicID, ownerID).Error; err != nil {
		t.Fatalf("seed legacy topic: %v", err)
	}
	if err := db.Exec(`INSERT INTO forum_replies (id, topic_id, user_id, content, floor_number) VALUES (?, ?, ?, ?, ?)`, replyID, topicID, authorID, "legacy", 1).Error; err != nil {
		t.Fatalf("seed legacy reply: %v", err)
	}

	if err := runUnifiedCommentStartupMigrations(db); err != nil {
		t.Fatalf("run unified comment startup migrations: %v", err)
	}
	var entry model.CommentEntry
	if err := db.First(&entry, "id = ?", replyID).Error; err != nil {
		t.Fatalf("expected legacy reply to be migrated: %v", err)
	}
}

func requireLegacyForumTables(t *testing.T, db interface {
	Exec(string, ...interface{}) *gorm.DB
}) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE forum_topics (id TEXT PRIMARY KEY, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ, deleted_at TIMESTAMPTZ, user_id TEXT NOT NULL, solved_reply_id TEXT)`,
		`CREATE TABLE forum_replies (id TEXT PRIMARY KEY, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ, deleted_at TIMESTAMPTZ, topic_id TEXT NOT NULL, user_id TEXT NOT NULL, parent_reply_id TEXT, content TEXT NOT NULL, floor_number INTEGER, is_solved BOOLEAN)`,
		`CREATE TABLE forum_likes (id TEXT PRIMARY KEY, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ, deleted_at TIMESTAMPTZ, user_id TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create legacy forum table: %v", err)
		}
	}
}

func TestCORSAllowsExplicitDevelopmentOriginsWithCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "development")
	t.Setenv("ALLOWED_ORIGINS", "https://studio.example")

	router := gin.New()
	router.Use(corsMiddleware(configuredAllowedOrigins()))
	router.GET("/ping", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	for _, origin := range []string{"http://localhost:5173", "https://studio.example"} {
		t.Run(origin, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ping", nil)
			req.Header.Set("Origin", origin)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
				t.Fatalf("ACAO = %q, want %q", got, origin)
			}
			if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
				t.Fatalf("credentials = %q, want true", got)
			}
		})
	}
}

func TestBootstrapOwnerFromEnvCreatesOwnerWhenConfigured(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, ownerBootstrapModels()...)
	t.Setenv("OWNER_USERNAME", "owner")
	t.Setenv("OWNER_EMAIL", "owner@example.com")
	t.Setenv("OWNER_PASSWORD", "change-me")

	if err := bootstrapOwnerFromEnv(db); err != nil {
		t.Fatalf("bootstrap owner: %v", err)
	}

	var user model.User
	if err := db.First(&user, "username = ?", "owner").Error; err != nil {
		t.Fatalf("reload owner: %v", err)
	}
	if user.Role != "owner" {
		t.Fatalf("expected role owner, got %s", user.Role)
	}
	if !user.IsActive {
		t.Fatal("expected owner to be active")
	}
	var channels []model.Channel
	if err := db.Where("user_id = ?", user.UUID).Find(&channels).Error; err != nil {
		t.Fatalf("find owner channels: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("expected one owner studio channel, got %d", len(channels))
	}
	var state model.UserStudioState
	if err := db.First(&state, "user_id = ?", user.UUID).Error; err != nil {
		t.Fatalf("find owner studio state: %v", err)
	}
	if state.ChannelID == nil || *state.ChannelID != channels[0].ID {
		t.Fatalf("expected current channel %s, got %#v", channels[0].ID, state.ChannelID)
	}
	var collections []model.ContentCollection
	if err := db.Where("channel_id = ? AND is_default = ?", channels[0].ID, true).Find(&collections).Error; err != nil {
		t.Fatalf("count default collection: %v", err)
	}
	if len(collections) != 1 {
		t.Fatalf("expected one mixed-content default collection, got %d", len(collections))
	}
}

func TestBootstrapOwnerFromEnvSkipsWhenNotConfigured(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, ownerBootstrapModels()...)
	t.Setenv("OWNER_USERNAME", "")
	t.Setenv("OWNER_EMAIL", "")
	t.Setenv("OWNER_PASSWORD", "")

	if err := bootstrapOwnerFromEnv(db); err != nil {
		t.Fatalf("bootstrap owner: %v", err)
	}

	var count int64
	if err := db.Model(&model.User{}).Count(&count).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no users, got %d", count)
	}
}

func TestBootstrapOwnerFromEnvSkipsPartialConfig(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, ownerBootstrapModels()...)
	t.Setenv("OWNER_USERNAME", "owner")
	t.Setenv("OWNER_EMAIL", "")
	t.Setenv("OWNER_PASSWORD", "")

	if err := bootstrapOwnerFromEnv(db); err != nil {
		t.Fatalf("expected partial owner config to be skipped, got %v", err)
	}

	var count int64
	if err := db.Model(&model.User{}).Count(&count).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no users, got %d", count)
	}
}

func TestBootstrapOwnerFromEnvDoesNotUpdateExistingOwnerByDefault(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, ownerBootstrapModels()...)
	existing := model.User{Username: "owner", Email: "owner@example.com", Password: "manual-hash", Role: "owner", IsActive: true}
	otherOwner := model.User{Username: "other", Email: "other@example.com", Password: "other-hash", Role: "owner", IsActive: true}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create existing owner: %v", err)
	}
	if err := db.Create(&otherOwner).Error; err != nil {
		t.Fatalf("create other owner: %v", err)
	}
	t.Setenv("OWNER_USERNAME", "owner")
	t.Setenv("OWNER_EMAIL", "owner@example.com")
	t.Setenv("OWNER_PASSWORD", "change-me")

	if err := bootstrapOwnerFromEnv(db); err != nil {
		t.Fatalf("bootstrap owner: %v", err)
	}

	var reloaded model.User
	if err := db.First(&reloaded, "uuid = ?", existing.UUID).Error; err != nil {
		t.Fatalf("reload existing owner: %v", err)
	}
	if reloaded.Password != "manual-hash" {
		t.Fatalf("expected existing password to remain manual-hash, got %q", reloaded.Password)
	}
	var reloadedOther model.User
	if err := db.First(&reloadedOther, "uuid = ?", otherOwner.UUID).Error; err != nil {
		t.Fatalf("reload other owner: %v", err)
	}
	if reloadedOther.Role != "owner" {
		t.Fatalf("expected other owner role to remain owner, got %q", reloadedOther.Role)
	}
}

func ownerBootstrapModels() []interface{} {
	return []interface{}{
		&model.User{},
		&model.UserSettings{},
		&model.Channel{},
		&model.Collection{},
		&model.ContentCollection{},
		&model.UserStudioState{},
		&model.StudioModuleSettings{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
		&model.BookmarkFolder{},
		&model.Playlist{},
	}
}

func TestBackfillInternalRSSFeedSourcesConvertsRelativeURLs(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.FeedSource{})

	user := model.User{
		Username: "fazong",
		Email:    "fazong@example.com",
		Password: "hashed",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	source := model.FeedSource{
		SourceType: "external_rss",
		RssURL:     "/api/feed/rss/fazong",
		Hash:       uuid.NewString(),
		Title:      "fazong rss",
		Provider:   "rss",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	backfillInternalRSSFeedSources(db)

	var updated model.FeedSource
	if err := db.First(&updated, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("reload source: %v", err)
	}

	if updated.SourceType != "internal_user" {
		t.Fatalf("expected source_type internal_user, got %s", updated.SourceType)
	}
	if updated.SourceID == nil || *updated.SourceID != user.UUID {
		t.Fatalf("expected source_id %s, got %v", user.UUID, updated.SourceID)
	}
	if updated.RssURL != "" {
		t.Fatalf("expected rss_url cleared, got %q", updated.RssURL)
	}
}

func TestBackfillInternalRSSFeedSourcesConvertsV1RelativeURLs(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.FeedSource{})

	user := model.User{Username: "v1user", Email: "v1@example.com", Password: "hashed"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	source := model.FeedSource{
		SourceType: "external_rss",
		RssURL:     "/api/v1/feed/rss/v1user",
		Hash:       uuid.NewString(),
		Title:      "v1 rss",
		Provider:   "rss",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	backfillInternalRSSFeedSources(db)

	var updated model.FeedSource
	if err := db.First(&updated, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("reload source: %v", err)
	}
	if updated.SourceType != "internal_user" {
		t.Fatalf("expected source_type internal_user, got %s", updated.SourceType)
	}
	if updated.SourceID == nil || *updated.SourceID != user.UUID {
		t.Fatalf("expected source_id %s, got %v", user.UUID, updated.SourceID)
	}
}

func TestBackfillInternalRSSFeedSourcesMergesIntoExistingCanonicalSource(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.FeedSource{}, &model.Subscription{})

	user := model.User{
		Username: "fazong",
		Email:    "fazong@example.com",
		Password: "hashed",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	canonical := model.FeedSource{
		SourceType: "internal_user",
		SourceID:   &user.UUID,
		Hash:       buildInternalFeedSourceHash("internal_user", user.UUID),
		Provider:   "internal",
		Title:      "canonical",
	}
	if err := db.Create(&canonical).Error; err != nil {
		t.Fatalf("create canonical source: %v", err)
	}

	legacy := model.FeedSource{
		SourceType: "external_rss",
		RssURL:     "/api/feed/rss/fazong",
		Hash:       uuid.NewString(),
		Title:      "legacy rss",
		Provider:   "rss",
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("create legacy source: %v", err)
	}

	subscription := model.Subscription{
		UserID:       user.UUID,
		FeedSourceID: legacy.ID,
		Title:        "legacy sub",
	}
	if err := db.Create(&subscription).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	backfillInternalRSSFeedSources(db)

	var updatedSubscription model.Subscription
	if err := db.First(&updatedSubscription, "id = ?", subscription.ID).Error; err != nil {
		t.Fatalf("reload subscription: %v", err)
	}
	if updatedSubscription.FeedSourceID != canonical.ID {
		t.Fatalf("expected subscription feed_source_id %s, got %s", canonical.ID, updatedSubscription.FeedSourceID)
	}

	var legacyCount int64
	if err := db.Model(&model.FeedSource{}).Where("id = ?", legacy.ID).Count(&legacyCount).Error; err != nil {
		t.Fatalf("count legacy source: %v", err)
	}
	if legacyCount != 0 {
		t.Fatalf("expected legacy source to be removed, count=%d", legacyCount)
	}
}

func TestBackfillInternalRSSFeedSourcesMergesDuplicateSubscriptions(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.FeedSource{}, &model.Subscription{})

	viewer := model.User{
		Username: "viewer",
		Email:    "viewer@example.com",
		Password: "hashed",
	}
	if err := db.Create(&viewer).Error; err != nil {
		t.Fatalf("create viewer: %v", err)
	}

	author := model.User{
		Username: "fazong",
		Email:    "fazong@example.com",
		Password: "hashed",
	}
	if err := db.Create(&author).Error; err != nil {
		t.Fatalf("create author: %v", err)
	}

	canonical := model.FeedSource{
		SourceType: "internal_user",
		SourceID:   &author.UUID,
		Hash:       buildInternalFeedSourceHash("internal_user", author.UUID),
		Provider:   "internal",
		Title:      "canonical",
	}
	if err := db.Create(&canonical).Error; err != nil {
		t.Fatalf("create canonical source: %v", err)
	}

	legacy := model.FeedSource{
		SourceType: "external_rss",
		RssURL:     "/api/feed/rss/fazong",
		Hash:       uuid.NewString(),
		Title:      "legacy rss",
		Provider:   "rss",
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("create legacy source: %v", err)
	}

	canonicalSubscription := model.Subscription{
		UserID:       viewer.UUID,
		FeedSourceID: canonical.ID,
		Title:        "canonical sub",
	}
	if err := db.Create(&canonicalSubscription).Error; err != nil {
		t.Fatalf("create canonical subscription: %v", err)
	}

	legacySubscription := model.Subscription{
		UserID:       viewer.UUID,
		FeedSourceID: legacy.ID,
		Title:        "legacy sub",
	}
	if err := db.Create(&legacySubscription).Error; err != nil {
		t.Fatalf("create legacy subscription: %v", err)
	}

	backfillInternalRSSFeedSources(db)

	var activeSubscriptions []model.Subscription
	if err := db.Where("user_id = ?", viewer.UUID).Find(&activeSubscriptions).Error; err != nil {
		t.Fatalf("load active subscriptions: %v", err)
	}
	if len(activeSubscriptions) != 1 {
		t.Fatalf("expected one active subscription after merge, got %d", len(activeSubscriptions))
	}
	if activeSubscriptions[0].FeedSourceID != canonical.ID {
		t.Fatalf("expected remaining subscription feed_source_id %s, got %s", canonical.ID, activeSubscriptions[0].FeedSourceID)
	}

	var legacyCount int64
	if err := db.Model(&model.FeedSource{}).Where("id = ?", legacy.ID).Count(&legacyCount).Error; err != nil {
		t.Fatalf("count legacy source: %v", err)
	}
	if legacyCount != 0 {
		t.Fatalf("expected legacy source to be removed, count=%d", legacyCount)
	}
}

func TestBackfillInternalRSSFeedSourcesSkipsUnknownUsers(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.FeedSource{})

	source := model.FeedSource{
		SourceType: "external_rss",
		RssURL:     "/api/feed/rss/missing-user",
		Hash:       uuid.NewString(),
		Title:      "missing user rss",
		Provider:   "rss",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	backfillInternalRSSFeedSources(db)

	var updated model.FeedSource
	if err := db.First(&updated, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("reload source: %v", err)
	}

	if updated.SourceType != "external_rss" {
		t.Fatalf("expected source_type external_rss, got %s", updated.SourceType)
	}
	if updated.RssURL != "/api/feed/rss/missing-user" {
		t.Fatalf("expected rss_url preserved, got %q", updated.RssURL)
	}
}
