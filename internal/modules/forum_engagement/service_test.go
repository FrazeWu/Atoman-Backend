package forum_engagement

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	coreservice "atoman/internal/service"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newForumEngagementTestService(t *testing.T) (*Service, *gorm.DB, model.User, model.User, model.ForumCategory) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumLike{},
		&model.ForumBookmark{},
		&model.ForumUserTrust{},
		&model.ForumGroup{},
		&model.ForumGroupMember{},
		&model.ForumCategoryPermission{},
		&model.DiscussionTarget{},
		&model.CommentEntry{},
		&model.CommentLike{},
	)
	author := model.User{Username: "engagement-author", Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true, CreatedAt: time.Now().UTC().Add(-4 * 24 * time.Hour)}
	liker := model.User{Username: "engagement-liker", Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&author).Error; err != nil {
		t.Fatalf("create author: %v", err)
	}
	if err := db.Create(&liker).Error; err != nil {
		t.Fatalf("create liker: %v", err)
	}
	category := model.ForumCategory{Name: uuid.NewString(), Color: "#000000"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	return NewService(db), db, author, liker, category
}

func TestPrivateForumEngagementRequiresCategoryVisibility(t *testing.T) {
	svc, db, author, _, category := newForumEngagementTestService(t)
	member := model.User{Username: "private-member-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	outsider := model.User{Username: "private-outsider-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	admin := model.User{Username: "private-admin-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	for _, user := range []*model.User{&member, &outsider, &admin} {
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	group := model.ForumGroup{Name: "private-" + uuid.NewString()}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.Create(&model.ForumGroupMember{GroupID: group.ID, UserID: member.UUID}).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}
	if err := db.Create(&model.ForumCategoryPermission{CategoryID: category.ID, GroupID: group.ID, CanView: true}).Error; err != nil {
		t.Fatalf("create permission: %v", err)
	}
	topic := model.ForumTopic{UserID: author.UUID, CategoryID: category.ID, Title: "private", Content: "body"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}
	actor := func(user model.User) authctx.CurrentUser {
		return authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
	}
	for name, action := range map[string]func() error{
		"topic like": func() error { _, err := svc.ToggleTopicLike(actor(outsider), topic.ID); return err },
		"bookmark":   func() error { _, err := svc.ToggleTopicBookmark(actor(outsider), topic.ID); return err },
	} {
		t.Run(name, func(t *testing.T) {
			var appErr *apperr.AppError
			if err := action(); !errors.As(err, &appErr) || appErr.HTTPStatus != 403 {
				t.Fatalf("expected 403, got %v", err)
			}
		})
	}
	var illegalLikes int64
	if err := db.Model(&model.ForumLike{}).Where("user_id = ?", outsider.UUID).Count(&illegalLikes).Error; err != nil || illegalLikes != 0 {
		t.Fatalf("expected no outsider likes, count=%d err=%v", illegalLikes, err)
	}
	if err := db.Model(&model.User{}).Where("uuid = ?", author.UUID).Update("created_at", time.Now().UTC().Add(-4*24*time.Hour)).Error; err != nil {
		t.Fatalf("set author age: %v", err)
	}
	thirdContribution := model.ForumTopic{UserID: author.UUID, CategoryID: category.ID, Title: "third", Content: "body"}
	if err := db.Create(&thirdContribution).Error; err != nil {
		t.Fatalf("create third contribution: %v", err)
	}
	trust, err := coreservice.NewForumTrustService(db).Evaluate(author.UUID)
	if err != nil || trust.Level != 0 {
		t.Fatalf("expected illegal likes not to affect trust, trust=%#v err=%v", trust, err)
	}
	if _, err := svc.ToggleTopicLike(actor(member), topic.ID); err != nil {
		t.Fatalf("member topic like: %v", err)
	}
	if _, err := svc.ToggleTopicBookmark(actor(admin), topic.ID); err != nil {
		t.Fatalf("admin bookmark: %v", err)
	}
}

func TestTopicLikeCanBeAddedAgainAfterSoftDeleteToggle(t *testing.T) {
	svc, db, author, liker, category := newForumEngagementTestService(t)
	topic := model.ForumTopic{UserID: author.UUID, CategoryID: category.ID, Title: "toggle", Content: "body"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}
	actor := authctx.CurrentUser{ID: liker.UUID, Role: liker.Role}
	for index, want := range []bool{true, false, true} {
		state, err := svc.ToggleTopicLike(actor, topic.ID)
		if err != nil || state.Liked != want {
			t.Fatalf("toggle %d: state=%#v err=%v", index, state, err)
		}
	}
	var stored model.ForumTopic
	if err := db.First(&stored, "id = ?", topic.ID).Error; err != nil {
		t.Fatalf("reload topic: %v", err)
	}
	if stored.LikeCount != 1 {
		t.Fatalf("expected like_count 1, got %d", stored.LikeCount)
	}
}

func TestNewTopicLikeTriggersAuthorTrustEvaluation(t *testing.T) {
	svc, db, author, liker, category := newForumEngagementTestService(t)
	var targetID uuid.UUID
	for index := 0; index < 3; index++ {
		topic := model.ForumTopic{UserID: author.UUID, CategoryID: category.ID, Title: "topic-" + uuid.NewString(), Content: "body"}
		if err := db.Create(&topic).Error; err != nil {
			t.Fatalf("create topic: %v", err)
		}
		targetID = topic.ID
	}
	state, err := svc.ToggleTopicLike(authctx.CurrentUser{ID: liker.UUID, Role: authctx.RoleUser}, targetID)
	if err != nil || !state.Liked {
		t.Fatalf("toggle like: state=%#v err=%v", state, err)
	}
	var trust model.ForumUserTrust
	if err := db.First(&trust, "user_id = ?", author.UUID).Error; err != nil {
		t.Fatalf("find author trust: %v", err)
	}
	if trust.Level != 1 {
		t.Fatalf("expected author to reach level 1, got %d", trust.Level)
	}
}

func TestTrustEvaluationFailureDoesNotRollbackNewLike(t *testing.T) {
	svc, db, author, liker, category := newForumEngagementTestService(t)
	topic := model.ForumTopic{UserID: author.UUID, CategoryID: category.ID, Title: "target", Content: "body"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}
	if err := db.Migrator().DropTable(&model.ForumUserTrust{}); err != nil {
		t.Fatalf("drop trust table: %v", err)
	}
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldWriter) })

	state, err := svc.ToggleTopicLike(authctx.CurrentUser{ID: liker.UUID, Role: authctx.RoleUser}, topic.ID)
	if err != nil || !state.Liked {
		t.Fatalf("expected like success despite trust failure: state=%#v err=%v", state, err)
	}
	var likeCount int64
	if err := db.Model(&model.ForumLike{}).Where("user_id = ? AND target_id = ?", liker.UUID, topic.ID).Count(&likeCount).Error; err != nil {
		t.Fatalf("count likes: %v", err)
	}
	if likeCount != 1 {
		t.Fatalf("expected persisted like, got %d", likeCount)
	}
	if !strings.Contains(logs.String(), "forum trust evaluation failed") {
		t.Fatalf("expected evaluation failure log, got %q", logs.String())
	}
}
