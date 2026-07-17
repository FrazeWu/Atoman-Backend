package forum_engagement

import (
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestTopicLikePostgresSupportsRelikeAndConcurrentToggles(t *testing.T) {
	db := newForumEngagementPostgresDB(t)
	author := model.User{Username: "pg-author-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	liker := model.User{Username: "pg-liker-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	for _, user := range []*model.User{&author, &liker} {
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	category := model.ForumCategory{Name: "pg-" + uuid.NewString(), Color: "#000000"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	topic := model.ForumTopic{UserID: author.UUID, CategoryID: category.ID, Title: "target", Content: "body"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}
	svc := NewService(db)
	actor := authctx.CurrentUser{ID: liker.UUID, Role: liker.Role}
	for index, want := range []bool{true, false, true} {
		state, err := svc.ToggleTopicLike(actor, topic.ID)
		if err != nil || state.Liked != want {
			t.Fatalf("sequential toggle %d: state=%#v err=%v", index, state, err)
		}
	}
	// Start concurrent toggles from an unliked state so two successful toggles must end unliked.
	if _, err := svc.ToggleTopicLike(actor, topic.ID); err != nil {
		t.Fatalf("prepare unliked state: %v", err)
	}
	installForumLikeCreateBarrier(t, db)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.ToggleTopicLike(actor, topic.ID)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent toggle: %v", err)
		}
	}
	var likeRows int64
	if err := db.Model(&model.ForumLike{}).Where("user_id = ? AND target_id = ?", liker.UUID, topic.ID).Count(&likeRows).Error; err != nil {
		t.Fatalf("count likes: %v", err)
	}
	var stored model.ForumTopic
	if err := db.First(&stored, "id = ?", topic.ID).Error; err != nil {
		t.Fatalf("load topic: %v", err)
	}
	if likeRows != 0 || stored.LikeCount != 0 {
		t.Fatalf("expected consistent unliked state, rows=%d like_count=%d", likeRows, stored.LikeCount)
	}
}

func installForumLikeCreateBarrier(t *testing.T, db *gorm.DB) {
	t.Helper()
	var arrivals int32
	ready := make(chan struct{})
	var once sync.Once
	if err := db.Callback().Create().Before("gorm:create").Register("test:forum_like_barrier", func(tx *gorm.DB) {
		if tx.Statement.Table != "forum_likes" {
			return
		}
		if atomic.AddInt32(&arrivals, 1) == 2 {
			once.Do(func() { close(ready) })
		}
		select {
		case <-ready:
		case <-time.After(500 * time.Millisecond):
		}
	}); err != nil {
		t.Fatalf("register like barrier: %v", err)
	}
}

func newForumEngagementPostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("FORUM_TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://atoman:atoman_secret@localhost:5432/postgres?sslmode=disable"
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	sqlDB, err := admin.DB()
	if err != nil || sqlDB.Ping() != nil {
		t.Skip("PostgreSQL unavailable")
	}
	if err := admin.Exec("CREATE EXTENSION IF NOT EXISTS ltree").Error; err != nil {
		t.Fatalf("enable ltree: %v", err)
	}
	schema := "forum_engagement_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error })
	parsed, _ := url.Parse(dsn)
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open schema db: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(8)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.ForumCategory{}, &model.ForumTopic{},
		&model.ForumLike{}, &model.ForumBookmark{}, &model.ForumUserTrust{},
		&model.ForumGroup{}, &model.ForumGroupMember{}, &model.ForumCategoryPermission{},
		&model.DiscussionTarget{}, &model.CommentEntry{}, &model.CommentLike{},
	); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	return db
}
