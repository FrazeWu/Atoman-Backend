package service

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func newForumTrustTestService(t *testing.T) (*ForumTrustService, *gorm.DB, model.User) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumLike{},
		&model.DiscussionTarget{},
		&model.CommentEntry{},
		&model.CommentLike{},
		&model.ForumUserTrust{},
	)
	user := model.User{
		Username: "trust-user", Email: uuid.NewString() + "@example.com", Password: "hash",
		Role: authctx.RoleUser, IsActive: true, CreatedAt: time.Now().UTC().Add(-90 * 24 * time.Hour),
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return NewForumTrustService(db), db, user
}

func createForumTrustComment(t *testing.T, db *gorm.DB, topicID, authorID uuid.UUID, content string) model.CommentEntry {
	t.Helper()
	var target model.DiscussionTarget
	if err := db.Where("kind = ? AND resource_id = ?", "forum_topic", topicID).First(&target).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		target = model.DiscussionTarget{Kind: "forum_topic", ResourceID: topicID, ResourceKey: topicID.String()}
		if err := db.Create(&target).Error; err != nil {
			t.Fatalf("create discussion target: %v", err)
		}
	} else if err != nil {
		t.Fatalf("find discussion target: %v", err)
	}
	comment := model.CommentEntry{TargetID: target.ID, AuthorID: authorID, Content: content, ContentHash: uuid.NewString(), Status: "active"}
	if err := db.Create(&comment).Error; err != nil {
		t.Fatalf("create forum comment: %v", err)
	}
	return comment
}

func seedTrustStats(t *testing.T, db *gorm.DB, user model.User, contributions, distinctLikers int) {
	t.Helper()
	category := model.ForumCategory{Name: "category-" + uuid.NewString(), Color: "#000000"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	var targets []model.ForumTopic
	for i := 0; i < contributions; i++ {
		topic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: fmt.Sprintf("topic %d", i), Content: "body"}
		if err := db.Create(&topic).Error; err != nil {
			t.Fatalf("create contribution %d: %v", i, err)
		}
		targets = append(targets, topic)
	}
	if len(targets) == 0 {
		return
	}
	for i := 0; i < distinctLikers; i++ {
		liker := model.User{Username: "liker-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
		if err := db.Create(&liker).Error; err != nil {
			t.Fatalf("create liker %d: %v", i, err)
		}
		like := model.ForumLike{UserID: liker.UUID, TargetType: "topic", TargetID: targets[i%len(targets)].ID}
		if err := db.Create(&like).Error; err != nil {
			t.Fatalf("create like %d: %v", i, err)
		}
	}
}

func TestForumTrustEvaluateThresholdBoundaries(t *testing.T) {
	tests := []struct {
		name          string
		ageDays       int
		contributions int
		likers        int
		wantLevel     int
	}{
		{name: "L0 below first boundary", ageDays: 3, contributions: 3, likers: 0, wantLevel: 0},
		{name: "L1 exact boundary", ageDays: 3, contributions: 3, likers: 1, wantLevel: 1},
		{name: "L2 exact boundary", ageDays: 14, contributions: 20, likers: 10, wantLevel: 2},
		{name: "L3 exact boundary", ageDays: 60, contributions: 100, likers: 50, wantLevel: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, db, user := newForumTrustTestService(t)
			createdAt := time.Now().UTC().Add(-time.Duration(tt.ageDays)*24*time.Hour - time.Minute)
			if err := db.Model(&model.User{}).Where("uuid = ?", user.UUID).Update("created_at", createdAt).Error; err != nil {
				t.Fatalf("set account age: %v", err)
			}
			seedTrustStats(t, db, user, tt.contributions, tt.likers)
			trust, err := svc.Evaluate(user.UUID)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if trust.Level != tt.wantLevel {
				t.Fatalf("expected level %d, got %d", tt.wantLevel, trust.Level)
			}
		})
	}
}

func TestForumTrustEvaluateCountsDistinctLikersAndExcludesSelfLikes(t *testing.T) {
	svc, db, user := newForumTrustTestService(t)
	seedTrustStats(t, db, user, 3, 1)
	var topic model.ForumTopic
	if err := db.Where("user_id = ?", user.UUID).First(&topic).Error; err != nil {
		t.Fatalf("find topic: %v", err)
	}
	var liker model.User
	if err := db.Where("uuid <> ?", user.UUID).First(&liker).Error; err != nil {
		t.Fatalf("find liker: %v", err)
	}
	secondTarget := createForumTrustComment(t, db, topic.ID, user.UUID, "reply")
	if err := db.Create(&model.CommentLike{UserID: liker.UUID, CommentID: secondTarget.ID}).Error; err != nil {
		t.Fatalf("create repeated liker: %v", err)
	}
	if err := db.Create(&model.ForumLike{UserID: user.UUID, TargetType: "topic", TargetID: topic.ID}).Error; err != nil {
		t.Fatalf("create self like: %v", err)
	}

	trust, err := svc.Evaluate(user.UUID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if trust.Level != 1 {
		t.Fatalf("expected one distinct external liker to qualify for L1, got %d", trust.Level)
	}
}

func TestForumTrustEvaluateNeverDowngrades(t *testing.T) {
	svc, db, user := newForumTrustTestService(t)
	existing := model.ForumUserTrust{UserID: user.UUID, Level: 3, EvaluatedAt: time.Now().UTC().Add(-time.Hour)}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create existing trust: %v", err)
	}
	trust, err := svc.Evaluate(user.UUID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if trust.Level != 3 {
		t.Fatalf("expected level 3 to remain, got %d", trust.Level)
	}
	if !trust.EvaluatedAt.After(existing.EvaluatedAt) {
		t.Fatal("expected evaluated_at to advance")
	}
}

func TestForumTrustIgnoresDeletedContributionsAndTheirLikes(t *testing.T) {
	svc, db, user := newForumTrustTestService(t)
	seedTrustStats(t, db, user, 3, 1)
	if err := db.Where("user_id = ?", user.UUID).Delete(&model.ForumTopic{}).Error; err != nil {
		t.Fatalf("delete contributions: %v", err)
	}
	trust, err := svc.Evaluate(user.UUID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if trust.Level != 0 {
		t.Fatalf("expected deleted content not to qualify, got level %d", trust.Level)
	}
}

func TestForumTrustIgnoresDeletedReplyAndItsLikes(t *testing.T) {
	svc, db, user := newForumTrustTestService(t)
	category := model.ForumCategory{Name: "deleted-reply-" + uuid.NewString(), Color: "#000000"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	topic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "topic", Content: "body"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}
	replies := []model.CommentEntry{
		createForumTrustComment(t, db, topic.ID, user.UUID, "active"),
		createForumTrustComment(t, db, topic.ID, user.UUID, "deleted"),
	}
	liker := model.User{Username: "deleted-reply-liker", Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&liker).Error; err != nil {
		t.Fatalf("create liker: %v", err)
	}
	if err := db.Create(&model.CommentLike{UserID: liker.UUID, CommentID: replies[1].ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Delete(&replies[1]).Error; err != nil {
		t.Fatalf("delete reply: %v", err)
	}
	trust, err := svc.Evaluate(user.UUID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if trust.Level != 0 {
		t.Fatalf("expected deleted reply and like not to qualify, got %d", trust.Level)
	}
}

func TestForumTrustIgnoresRepliesAndLikesWhoseTopicIsDeleted(t *testing.T) {
	t.Run("reply contributions require active topic", func(t *testing.T) {
		svc, db, user := newForumTrustTestService(t)
		category := model.ForumCategory{Name: "deleted-parent-" + uuid.NewString(), Color: "#000000"}
		if err := db.Create(&category).Error; err != nil {
			t.Fatalf("create category: %v", err)
		}
		activeTopic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "active", Content: "body"}
		deletedTopic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "deleted", Content: "body"}
		for _, topic := range []*model.ForumTopic{&activeTopic, &deletedTopic} {
			if err := db.Create(topic).Error; err != nil {
				t.Fatalf("create topic: %v", err)
			}
		}
		for index := 0; index < 3; index++ {
			createForumTrustComment(t, db, deletedTopic.ID, user.UUID, fmt.Sprintf("reply-%d", index))
		}
		liker := model.User{Username: "active-topic-liker", Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
		if err := db.Create(&liker).Error; err != nil {
			t.Fatalf("create liker: %v", err)
		}
		if err := db.Create(&model.ForumLike{UserID: liker.UUID, TargetType: "topic", TargetID: activeTopic.ID}).Error; err != nil {
			t.Fatalf("create active like: %v", err)
		}
		if err := db.Delete(&deletedTopic).Error; err != nil {
			t.Fatalf("delete topic: %v", err)
		}
		trust, err := svc.Evaluate(user.UUID)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if trust.Level != 0 {
			t.Fatalf("expected replies under deleted topic not to count, got %d", trust.Level)
		}
	})

	t.Run("reply likes require active topic and return after restore", func(t *testing.T) {
		svc, db, user := newForumTrustTestService(t)
		category := model.ForumCategory{Name: "deleted-like-parent-" + uuid.NewString(), Color: "#000000"}
		if err := db.Create(&category).Error; err != nil {
			t.Fatalf("create category: %v", err)
		}
		for index := 0; index < 3; index++ {
			topic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: fmt.Sprintf("active-%d", index), Content: "body"}
			if err := db.Create(&topic).Error; err != nil {
				t.Fatalf("create active topic: %v", err)
			}
		}
		deletedTopic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "liked reply host", Content: "body"}
		if err := db.Create(&deletedTopic).Error; err != nil {
			t.Fatalf("create deleted topic: %v", err)
		}
		reply := createForumTrustComment(t, db, deletedTopic.ID, user.UUID, "liked")
		liker := model.User{Username: "deleted-parent-liker", Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
		if err := db.Create(&liker).Error; err != nil {
			t.Fatalf("create liker: %v", err)
		}
		if err := db.Create(&model.CommentLike{UserID: liker.UUID, CommentID: reply.ID}).Error; err != nil {
			t.Fatalf("create reply like: %v", err)
		}
		if err := db.Delete(&deletedTopic).Error; err != nil {
			t.Fatalf("delete topic: %v", err)
		}
		trust, err := svc.Evaluate(user.UUID)
		if err != nil {
			t.Fatalf("evaluate deleted parent: %v", err)
		}
		if trust.Level != 0 {
			t.Fatalf("expected reply like under deleted topic not to count, got %d", trust.Level)
		}
		if err := db.Unscoped().Model(&model.ForumTopic{}).Where("id = ?", deletedTopic.ID).Update("deleted_at", nil).Error; err != nil {
			t.Fatalf("restore topic: %v", err)
		}
		trust, err = svc.Evaluate(user.UUID)
		if err != nil {
			t.Fatalf("evaluate restored parent: %v", err)
		}
		if trust.Level != 1 {
			t.Fatalf("expected restored reply like to count, got %d", trust.Level)
		}
	})
}

func TestForumTrustEvaluateSerializedSQLiteCallsRemainIdempotent(t *testing.T) {
	svc, db, user := newForumTrustTestService(t)
	seedTrustStats(t, db, user, 3, 1)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Evaluate(user.UUID)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent evaluate: %v", err)
		}
	}
	var count int64
	if err := db.Model(&model.ForumUserTrust{}).Where("user_id = ?", user.UUID).Count(&count).Error; err != nil {
		t.Fatalf("count trust rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one trust row, got %d", count)
	}
}

func TestForumTrustEvaluateConcurrentPostgresUpsertIsSingleRow(t *testing.T) {
	db := newForumTrustPostgresDB(t)
	user := model.User{Username: "trust-pg-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true, CreatedAt: time.Now().UTC().Add(-4 * 24 * time.Hour)}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	seedTrustStats(t, db, user, 3, 1)
	svc := NewForumTrustService(db)
	start := make(chan struct{})
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for index := 0; index < 12; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			trust, err := svc.Evaluate(user.UUID)
			if err == nil && trust.Level != 1 {
				err = fmt.Errorf("expected level 1, got %d", trust.Level)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent evaluate: %v", err)
		}
	}
	var rows []model.ForumUserTrust
	if err := db.Where("user_id = ?", user.UUID).Find(&rows).Error; err != nil {
		t.Fatalf("load trust rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Level != 1 {
		t.Fatalf("expected one level-1 row, got %#v", rows)
	}
}

func newForumTrustPostgresDB(t *testing.T) *gorm.DB {
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
	schema := "forum_trust_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
		sqlDB.SetMaxOpenConns(16)
	}
	if err := db.AutoMigrate(&model.User{}, &model.ForumCategory{}, &model.ForumTopic{}, &model.ForumLike{}, &model.DiscussionTarget{}, &model.CommentEntry{}, &model.CommentLike{}, &model.ForumUserTrust{}); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	return db
}

func TestForumTrustCreateRateLimitsByLevel(t *testing.T) {
	topicLimits := []int{3, 10, 30, 100}
	replyLimits := []int{20, 60, 180, 600}
	for level := 0; level <= 3; level++ {
		t.Run(fmt.Sprintf("level %d topic", level), func(t *testing.T) {
			svc, db, user := newForumTrustTestService(t)
			if err := db.Create(&model.ForumUserTrust{UserID: user.UUID, Level: level, EvaluatedAt: time.Now().UTC()}).Error; err != nil {
				t.Fatalf("create trust: %v", err)
			}
			category := model.ForumCategory{Name: uuid.NewString(), Color: "#000000"}
			if err := db.Create(&category).Error; err != nil {
				t.Fatalf("create category: %v", err)
			}
			for i := 0; i < topicLimits[level]-1; i++ {
				if err := db.Create(&model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: fmt.Sprintf("t%d", i), Content: "body"}).Error; err != nil {
					t.Fatalf("create topic %d: %v", i, err)
				}
			}
			if err := svc.CheckCreateTopic(authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleUser}, "allowed", "body"); err != nil {
				t.Fatalf("expected one remaining topic slot, got %v", err)
			}
			if err := db.Create(&model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "last-slot", Content: "body"}).Error; err != nil {
				t.Fatalf("fill last topic slot: %v", err)
			}
			err := svc.CheckCreateTopic(authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleUser}, "new", "body")
			assertTrustAppError(t, err, 429, "forum.topic_rate_limited")
		})

		t.Run(fmt.Sprintf("level %d reply", level), func(t *testing.T) {
			svc, db, user := newForumTrustTestService(t)
			if err := db.Create(&model.ForumUserTrust{UserID: user.UUID, Level: level, EvaluatedAt: time.Now().UTC()}).Error; err != nil {
				t.Fatalf("create trust: %v", err)
			}
			category := model.ForumCategory{Name: uuid.NewString(), Color: "#000000"}
			if err := db.Create(&category).Error; err != nil {
				t.Fatalf("create category: %v", err)
			}
			topic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "host", Content: "body"}
			if err := db.Create(&topic).Error; err != nil {
				t.Fatalf("create topic: %v", err)
			}
			for i := 0; i < replyLimits[level]-1; i++ {
				createForumTrustComment(t, db, topic.ID, user.UUID, fmt.Sprintf("r%d", i))
			}
			if err := svc.CheckCreateReply(authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleUser}, "allowed reply"); err != nil {
				t.Fatalf("expected one remaining reply slot, got %v", err)
			}
			createForumTrustComment(t, db, topic.ID, user.UUID, "last-slot")
			err := svc.CheckCreateReply(authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleUser}, "new reply")
			assertTrustAppError(t, err, 429, "forum.reply_rate_limited")
		})
	}
}

func TestForumTrustRateLimitCountsSoftDeletedAndPrivilegedRolesBypass(t *testing.T) {
	svc, db, user := newForumTrustTestService(t)
	category := model.ForumCategory{Name: uuid.NewString(), Color: "#000000"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	for i := 0; i < 3; i++ {
		topic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: fmt.Sprintf("deleted %d", i), Content: "body"}
		if err := db.Create(&topic).Error; err != nil {
			t.Fatalf("create topic: %v", err)
		}
		if err := db.Delete(&topic).Error; err != nil {
			t.Fatalf("soft delete topic: %v", err)
		}
	}
	assertTrustAppError(t, svc.CheckCreateTopic(authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleUser}, "new", "body"), 429, "forum.topic_rate_limited")
	for _, role := range []string{authctx.RoleModerator, authctx.RoleAdmin, authctx.RoleOwner} {
		if err := svc.CheckCreateTopic(authctx.CurrentUser{ID: user.UUID, Role: role}, "new", "body"); err != nil {
			t.Fatalf("expected %s to bypass frequency limit, got %v", role, err)
		}
	}
}

func TestForumTrustRejectsNormalizedDuplicatesAndRuneLengthOverflow(t *testing.T) {
	svc, db, user := newForumTrustTestService(t)
	category := model.ForumCategory{Name: uuid.NewString(), Color: "#000000"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	topic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "  SAME\nTitle ", Content: "Hello   WORLD"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}
	createForumTrustComment(t, db, topic.ID, user.UUID, "Same\n reply")

	assertTrustAppError(t, svc.CheckCreateTopic(authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleOwner}, "same title", " hello world "), 409, "forum.duplicate_topic")
	assertTrustAppError(t, svc.CheckCreateReply(authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleOwner}, " same  REPLY "), 409, "forum.duplicate_reply")
	unicodeTopic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "Ｆｏｏ", Content: "cafe\u0301"}
	if err := db.Create(&unicodeTopic).Error; err != nil {
		t.Fatalf("create unicode topic: %v", err)
	}
	assertTrustAppError(t, svc.CheckCreateTopic(authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleOwner}, "foo", "café"), 409, "forum.duplicate_topic")
	createForumTrustComment(t, db, topic.ID, user.UUID, "ze\u200bro")
	assertTrustAppError(t, svc.CheckCreateReply(authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleOwner}, "zero"), 409, "forum.duplicate_reply")
	assertTrustAppError(t, svc.CheckCreateTopic(authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleOwner}, string(make([]rune, 201)), "body"), 400, "forum.topic_title_too_long")
	assertTrustAppError(t, svc.CheckCreateTopic(authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleOwner}, "title", string(make([]rune, 50001))), 400, "forum.topic_content_too_long")
	assertTrustAppError(t, svc.CheckCreateReply(authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleOwner}, string(make([]rune, 20001))), 400, "forum.reply_content_too_long")
}

func assertTrustAppError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected app error %s, got %v", code, err)
	}
	if appErr.HTTPStatus != status || appErr.Code != code {
		t.Fatalf("expected status/code %d/%s, got %d/%s", status, code, appErr.HTTPStatus, appErr.Code)
	}
}
