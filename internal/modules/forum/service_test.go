package forum

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"gorm.io/gorm"
)

func newForumTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()

	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumReply{},
	)

	user := model.User{
		Username: "owner",
		Email:    "owner@example.com",
		Password: "hash",
		Role:     authctx.RoleUser,
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	return NewService(db), db, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
}

func TestDeleteReplyRecalculatesLatestReplyDerivedFields(t *testing.T) {
	svc, db, user := newForumTestService(t)

	topic := createForumTestTopic(t, db, user)

	reply1 := model.ForumReply{
		Base: model.Base{
			CreatedAt: time.Date(2026, 6, 28, 14, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 6, 28, 14, 0, 0, 0, time.UTC),
		},
		TopicID:     topic.ID,
		UserID:      user.ID,
		Content:     "solution reply",
		FloorNumber: 1,
		IsSolved:    true,
	}
	reply2 := model.ForumReply{
		Base: model.Base{
			CreatedAt: time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC),
		},
		TopicID:     topic.ID,
		UserID:      user.ID,
		Content:     "latest reply",
		FloorNumber: 2,
	}
	if err := db.Create(&reply1).Error; err != nil {
		t.Fatalf("create reply1: %v", err)
	}
	if err := db.Create(&reply2).Error; err != nil {
		t.Fatalf("create reply2: %v", err)
	}

	topic.ReplyCount = 2
	topic.IsSolved = true
	topic.SolvedReplyID = &reply1.ID
	topic.LastReplyAt = ptrTime(time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC))
	if err := db.Save(&topic).Error; err != nil {
		t.Fatalf("seed topic derived fields: %v", err)
	}

	if err := svc.DeleteReply(user, reply2.ID); err != nil {
		t.Fatalf("delete reply: %v", err)
	}

	var updatedTopic model.ForumTopic
	if err := db.First(&updatedTopic, "id = ?", topic.ID).Error; err != nil {
		t.Fatalf("reload topic: %v", err)
	}
	if updatedTopic.ReplyCount != 1 {
		t.Fatalf("expected reply_count 1, got %d", updatedTopic.ReplyCount)
	}
	if updatedTopic.LastReplyAt == nil || !updatedTopic.LastReplyAt.Equal(reply1.CreatedAt) {
		t.Fatalf("expected last_reply_at %v, got %#v", reply1.CreatedAt, updatedTopic.LastReplyAt)
	}
	if !updatedTopic.IsSolved {
		t.Fatalf("expected solved flag to stay true, got %#v", updatedTopic)
	}
	if updatedTopic.SolvedReplyID == nil || *updatedTopic.SolvedReplyID != reply1.ID {
		t.Fatalf("expected solved reply id %v, got %#v", reply1.ID, updatedTopic.SolvedReplyID)
	}
}

func TestDeleteReplyRecalculatesSolvedDerivedFields(t *testing.T) {
	svc, db, user := newForumTestService(t)

	topic := createForumTestTopic(t, db, user)

	reply1 := model.ForumReply{
		Base: model.Base{
			CreatedAt: time.Date(2026, 6, 28, 14, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 6, 28, 14, 0, 0, 0, time.UTC),
		},
		TopicID:     topic.ID,
		UserID:      user.ID,
		Content:     "solution reply",
		FloorNumber: 1,
		IsSolved:    true,
	}
	reply2 := model.ForumReply{
		Base: model.Base{
			CreatedAt: time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC),
		},
		TopicID:     topic.ID,
		UserID:      user.ID,
		Content:     "latest reply",
		FloorNumber: 2,
	}
	if err := db.Create(&reply1).Error; err != nil {
		t.Fatalf("create reply1: %v", err)
	}
	if err := db.Create(&reply2).Error; err != nil {
		t.Fatalf("create reply2: %v", err)
	}

	topic.ReplyCount = 2
	topic.IsSolved = true
	topic.SolvedReplyID = &reply1.ID
	topic.LastReplyAt = ptrTime(time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC))
	if err := db.Save(&topic).Error; err != nil {
		t.Fatalf("seed topic derived fields: %v", err)
	}

	if err := svc.DeleteReply(user, reply1.ID); err != nil {
		t.Fatalf("delete reply: %v", err)
	}

	var updatedTopic model.ForumTopic
	if err := db.First(&updatedTopic, "id = ?", topic.ID).Error; err != nil {
		t.Fatalf("reload topic: %v", err)
	}
	if updatedTopic.ReplyCount != 1 {
		t.Fatalf("expected reply_count 1, got %d", updatedTopic.ReplyCount)
	}
	if updatedTopic.LastReplyAt == nil || !updatedTopic.LastReplyAt.Equal(reply2.CreatedAt) {
		t.Fatalf("expected last_reply_at %v, got %#v", reply2.CreatedAt, updatedTopic.LastReplyAt)
	}
	if updatedTopic.IsSolved {
		t.Fatalf("expected solved flag to clear, got %#v", updatedTopic)
	}
	if updatedTopic.SolvedReplyID != nil {
		t.Fatalf("expected solved reply id to clear, got %#v", updatedTopic.SolvedReplyID)
	}
}

func createForumTestTopic(t *testing.T, db *gorm.DB, user authctx.CurrentUser) model.ForumTopic {
	t.Helper()

	category := model.ForumCategory{Name: "General", Description: "general", Color: "#111111"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}

	topic := model.ForumTopic{
		UserID:     user.ID,
		CategoryID: category.ID,
		Title:      "Solved topic",
		Content:    "topic content",
	}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}
	return topic
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
