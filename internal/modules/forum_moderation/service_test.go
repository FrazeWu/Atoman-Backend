package forum_moderation

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"gorm.io/gorm"
)

func newForumModerationTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()

	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumReply{},
		&model.ForumModeratorAssignment{},
		&model.ForumReport{},
		&model.CategoryRequest{},
	)

	admin := model.User{Username: "admin", Email: "admin@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}

	return NewService(db), db, authctx.CurrentUser{ID: admin.UUID, Username: admin.Username, Role: authctx.RoleAdmin}
}

func TestHideReplyRecalculatesTopicDerivedFields(t *testing.T) {
	svc, db, admin := newForumModerationTestService(t)

	category := model.ForumCategory{Name: "General", Description: "general", Color: "#111111"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	topic := model.ForumTopic{
		UserID:      admin.ID,
		CategoryID:  category.ID,
		Title:       "Solved topic",
		Content:     "topic content",
		ReplyCount:  2,
		IsSolved:    true,
		LastReplyAt: ptrTime(time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC)),
	}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}

	topicID := topic.ID
	reply1 := model.ForumReply{
		Base: model.Base{
			CreatedAt: time.Date(2026, 6, 28, 14, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 6, 28, 14, 0, 0, 0, time.UTC),
		},
		TopicID:  topicID,
		UserID:   admin.ID,
		Content:  "first reply",
		IsSolved: false,
	}
	reply2 := model.ForumReply{
		Base: model.Base{
			CreatedAt: time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC),
		},
		TopicID:  topicID,
		UserID:   admin.ID,
		Content:  "solution reply",
		IsSolved: true,
	}
	if err := db.Create(&reply1).Error; err != nil {
		t.Fatalf("create reply1: %v", err)
	}
	if err := db.Create(&reply2).Error; err != nil {
		t.Fatalf("create reply2: %v", err)
	}

	mod := model.User{Username: "mod", Email: "mod@example.com", Password: "hash", Role: authctx.RoleModerator, IsActive: true}
	if err := db.Create(&mod).Error; err != nil {
		t.Fatalf("create moderator: %v", err)
	}
	assignment := model.ForumModeratorAssignment{
		UserID:       mod.UUID,
		CategoryID:   &category.ID,
		CanLockTopic: true,
	}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	moderator := authctx.CurrentUser{ID: mod.UUID, Username: mod.Username, Role: authctx.RoleModerator}

	if _, err := svc.HideReply(moderator, reply2.ID); err != nil {
		t.Fatalf("hide reply: %v", err)
	}

	var hiddenTopic model.ForumTopic
	if err := db.Unscoped().First(&hiddenTopic, "id = ?", topicID).Error; err != nil {
		t.Fatalf("reload topic after hide: %v", err)
	}
	if hiddenTopic.ReplyCount != 1 {
		t.Fatalf("expected reply_count 1 after hide, got %d", hiddenTopic.ReplyCount)
	}
	if hiddenTopic.IsSolved {
		t.Fatalf("expected solved flag to clear after hiding solved reply, got %#v", hiddenTopic)
	}
	if hiddenTopic.SolvedReplyID != nil {
		t.Fatalf("expected solved reply id to clear after hide, got %#v", hiddenTopic.SolvedReplyID)
	}
	if hiddenTopic.LastReplyAt == nil || !hiddenTopic.LastReplyAt.Equal(reply1.CreatedAt) {
		t.Fatalf("expected last_reply_at to track remaining reply, got %#v", hiddenTopic.LastReplyAt)
	}

	if _, err := svc.RestoreReply(moderator, reply2.ID); err != nil {
		t.Fatalf("restore reply: %v", err)
	}

	var restoredTopic model.ForumTopic
	if err := db.Unscoped().First(&restoredTopic, "id = ?", topicID).Error; err != nil {
		t.Fatalf("reload topic after restore: %v", err)
	}
	if restoredTopic.ReplyCount != 2 {
		t.Fatalf("expected reply_count 2 after restore, got %d", restoredTopic.ReplyCount)
	}
	if !restoredTopic.IsSolved {
		t.Fatalf("expected solved flag to restore, got %#v", restoredTopic)
	}
	if restoredTopic.SolvedReplyID == nil || *restoredTopic.SolvedReplyID != reply2.ID {
		t.Fatalf("expected solved reply id to restore, got %#v", restoredTopic.SolvedReplyID)
	}
	if restoredTopic.LastReplyAt == nil || !restoredTopic.LastReplyAt.Equal(reply2.CreatedAt) {
		t.Fatalf("expected last_reply_at to restore latest reply, got %#v", restoredTopic.LastReplyAt)
	}
}

func TestHideTopicClearsAndRestoreTopicRecalculatesDerivedFields(t *testing.T) {
	svc, db, admin := newForumModerationTestService(t)

	category := model.ForumCategory{Name: "Support", Description: "support", Color: "#222222"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}

	topic := model.ForumTopic{
		UserID:        admin.ID,
		CategoryID:    category.ID,
		Title:         "Hidden topic",
		Content:       "topic content",
		ReplyCount:    2,
		IsSolved:      true,
		SolvedReplyID: nil,
		LastReplyAt:   ptrTime(time.Date(2026, 6, 29, 15, 0, 0, 0, time.UTC)),
	}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}

	reply1 := model.ForumReply{
		Base: model.Base{
			CreatedAt: time.Date(2026, 6, 29, 14, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 6, 29, 14, 0, 0, 0, time.UTC),
		},
		TopicID: topic.ID,
		UserID:  admin.ID,
		Content: "first reply",
	}
	alreadyHiddenReply := model.ForumReply{
		Base: model.Base{
			CreatedAt: time.Date(2026, 6, 29, 14, 30, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 6, 29, 14, 30, 0, 0, time.UTC),
		},
		TopicID: topic.ID,
		UserID:  admin.ID,
		Content: "already hidden reply",
	}
	reply2 := model.ForumReply{
		Base: model.Base{
			CreatedAt: time.Date(2026, 6, 29, 15, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 6, 29, 15, 0, 0, 0, time.UTC),
		},
		TopicID:  topic.ID,
		UserID:   admin.ID,
		Content:  "solution reply",
		IsSolved: true,
	}
	if err := db.Create(&reply1).Error; err != nil {
		t.Fatalf("create reply1: %v", err)
	}
	if err := db.Create(&alreadyHiddenReply).Error; err != nil {
		t.Fatalf("create already hidden reply: %v", err)
	}
	if err := db.Delete(&alreadyHiddenReply).Error; err != nil {
		t.Fatalf("hide already hidden reply: %v", err)
	}
	if err := db.Create(&reply2).Error; err != nil {
		t.Fatalf("create reply2: %v", err)
	}
	if err := db.Model(&topic).Update("solved_reply_id", reply2.ID).Error; err != nil {
		t.Fatalf("seed solved reply id: %v", err)
	}

	if _, err := svc.HideTopic(admin, topic.ID); err != nil {
		t.Fatalf("hide topic: %v", err)
	}

	var hiddenTopic model.ForumTopic
	if err := db.Unscoped().First(&hiddenTopic, "id = ?", topic.ID).Error; err != nil {
		t.Fatalf("reload hidden topic: %v", err)
	}
	if hiddenTopic.ReplyCount != 0 {
		t.Fatalf("expected hidden topic reply_count 0, got %d", hiddenTopic.ReplyCount)
	}
	if hiddenTopic.LastReplyAt != nil {
		t.Fatalf("expected hidden topic last_reply_at to clear, got %#v", hiddenTopic.LastReplyAt)
	}
	if hiddenTopic.IsSolved || hiddenTopic.SolvedReplyID != nil {
		t.Fatalf("expected hidden topic solved state to clear, got %#v", hiddenTopic)
	}

	if _, err := svc.RestoreTopic(admin, topic.ID); err != nil {
		t.Fatalf("restore topic: %v", err)
	}

	var restoredTopic model.ForumTopic
	if err := db.First(&restoredTopic, "id = ?", topic.ID).Error; err != nil {
		t.Fatalf("reload restored topic: %v", err)
	}
	if restoredTopic.ReplyCount != 2 {
		t.Fatalf("expected restored topic reply_count 2, got %d", restoredTopic.ReplyCount)
	}
	if restoredTopic.LastReplyAt == nil || !restoredTopic.LastReplyAt.Equal(reply2.CreatedAt) {
		t.Fatalf("expected restored topic last_reply_at to use latest reply, got %#v", restoredTopic.LastReplyAt)
	}
	if !restoredTopic.IsSolved || restoredTopic.SolvedReplyID == nil || *restoredTopic.SolvedReplyID != reply2.ID {
		t.Fatalf("expected restored topic solved state to recalculate, got %#v", restoredTopic)
	}
	var stillHiddenReply model.ForumReply
	if err := db.Unscoped().First(&stillHiddenReply, "id = ?", alreadyHiddenReply.ID).Error; err != nil {
		t.Fatalf("reload already hidden reply: %v", err)
	}
	if !stillHiddenReply.DeletedAt.Valid {
		t.Fatalf("expected already hidden reply to stay hidden after restoring topic")
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
