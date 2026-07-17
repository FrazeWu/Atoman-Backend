package forum

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/forum_moderation"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func newForumConcurrencyPostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("FORUM_TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://atoman:atoman_secret@localhost:5432/postgres?sslmode=disable"
	}
	adminDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	adminSQL, err := adminDB.DB()
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	if err := adminSQL.Ping(); err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	if err := adminDB.Exec("CREATE EXTENSION IF NOT EXISTS ltree").Error; err != nil {
		t.Fatalf("enable ltree: %v", err)
	}
	schema := "forum_concurrency_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := adminDB.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _ = adminDB.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error })

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open schema database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("schema sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(5)
	if err := db.AutoMigrate(
		&model.User{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumLike{},
		&model.ForumUserTrust{},
		&model.ForumGroup{},
		&model.ForumGroupMember{},
		&model.ForumCategoryPermission{},
		&model.ForumUserModerationAction{},
		&model.ForumModeratorAssignment{},
		&model.ForumReport{},
		&model.ForumFollow{},
		&model.DiscussionTarget{},
		&model.CommentEntry{},
		&model.CommentLike{},
	); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	return db
}

func TestConcurrentDuplicateReportCreatesAtMostOneRowInPostgres(t *testing.T) {
	db := newForumConcurrencyPostgresDB(t)
	user, category := seedForumConcurrencyUser(t, db, authctx.RoleOwner)
	topic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "reported", Content: "body"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatal(err)
	}
	installConcurrentCreateBarrier(t, db, "forum_reports")
	service := forum_moderation.NewService(db)
	actor := authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := service.CreateReport(actor, forum_moderation.CreateReportRequest{TargetType: "topic", TargetID: topic.ID, Reason: "spam"})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	assertConcurrentForumResults(t, errs, "forum.report_exists")
	var count int64
	if err := db.Model(&model.ForumReport{}).Where("user_id = ? AND target_type = ? AND target_id = ?", user.UUID, "topic", topic.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one report, got %d", count)
	}
}

func seedForumConcurrencyUser(t *testing.T, db *gorm.DB, role string) (model.User, model.ForumCategory) {
	t.Helper()
	user := model.User{Username: "concurrent-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", Role: role, IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	category := model.ForumCategory{Name: "category-" + uuid.NewString(), Color: "#000000"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	return user, category
}

func TestCreateTopicSerializesLastRateLimitSlotInPostgres(t *testing.T) {
	db := newForumConcurrencyPostgresDB(t)
	user, category := seedForumConcurrencyUser(t, db, authctx.RoleUser)
	for index := 0; index < 2; index++ {
		topic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: fmt.Sprintf("seed-%d", index), Content: "body"}
		if err := db.Create(&topic).Error; err != nil {
			t.Fatalf("create seed topic: %v", err)
		}
	}
	installConcurrentCreateBarrier(t, db, "forum_topics")

	service := NewService(db)
	actor := authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, err := service.CreateTopic(actor, CreateTopicRequest{CategoryID: category.ID, Title: fmt.Sprintf("last-slot-%d", index), Content: "body"})
			errs <- err
		}(index)
	}
	close(start)
	wg.Wait()
	close(errs)

	assertConcurrentForumResults(t, errs, "forum.topic_rate_limited")
	var count int64
	if err := db.Model(&model.ForumTopic{}).Where("user_id = ?", user.UUID).Count(&count).Error; err != nil {
		t.Fatalf("count topics: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected exactly three topics, got %d", count)
	}
}

func installConcurrentCreateBarrier(t *testing.T, db *gorm.DB, table string) {
	t.Helper()
	var arrivals int32
	ready := make(chan struct{})
	var once sync.Once
	name := "test:concurrent_create_barrier:" + table
	if err := db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table != table {
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
		t.Fatalf("register create barrier: %v", err)
	}
}

func assertConcurrentForumResults(t *testing.T, errs <-chan error, wantCode string) {
	t.Helper()
	successes := 0
	wantedErrors := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		var appErr *apperr.AppError
		if errors.As(err, &appErr) && appErr.Code == wantCode {
			wantedErrors++
			continue
		}
		t.Fatalf("unexpected concurrent error: %v", err)
	}
	if successes != 1 || wantedErrors != 1 {
		t.Fatalf("expected one success and one %s, got successes=%d wanted_errors=%d", wantCode, successes, wantedErrors)
	}
}
