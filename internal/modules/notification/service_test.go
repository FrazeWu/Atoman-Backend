package notification

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestServiceListNotificationsPrefersTypeOverCategory(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Notification{}, &model.NotificationPreference{}, &model.NotificationMute{})
	user := model.User{Username: "recipient", Email: "recipient@example.test", Password: "test", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create notification recipient: %v", err)
	}
	userID := user.UUID
	if err := db.Create(&[]model.Notification{
		{RecipientID: userID, Type: "comment_like", SourceType: "test", SourceID: uuid.New()},
		{RecipientID: userID, Type: "comment_reply", SourceType: "test", SourceID: uuid.New()},
	}).Error; err != nil {
		t.Fatalf("create notifications: %v", err)
	}

	items, total, err := NewService(db).ListNotifications(authctx.CurrentUser{ID: userID}, ListQuery{Type: "comment_like", Category: "reply"})
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Type != "comment_like" {
		t.Fatalf("expected exact type to win over category, got total=%d items=%#v", total, items)
	}
}

func TestServicePublishAnnouncementDeliversToActiveUsersAndPublishesAfterCommit(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Notification{}, &model.NotificationPreference{}, &model.NotificationMute{})
	admin := model.User{Username: "announcement-admin", Email: "announcement-admin@example.test", Password: "test", Role: authctx.RoleAdmin, IsActive: true}
	recipient := model.User{Username: "announcement-recipient", Email: "announcement-recipient@example.test", Password: "test", Role: authctx.RoleUser, IsActive: true}
	inactive := model.User{Username: "announcement-inactive", Email: "announcement-inactive@example.test", Password: "test", Role: authctx.RoleUser, IsActive: false}
	for _, user := range []*model.User{&admin, &recipient, &inactive} {
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("create announcement user: %v", err)
		}
	}

	service := NewService(db)
	pushed := make([]model.Notification, 0)
	service.SetNotificationPublisher(func(_ uuid.UUID, notification *model.Notification) {
		pushed = append(pushed, *notification)
	})

	delivered, err := service.PublishAnnouncement(
		authctx.CurrentUser{ID: admin.UUID, Username: admin.Username, Role: admin.Role},
		PublishAnnouncementInput{Title: "系统维护", Body: "周日凌晨进行例行维护", Path: "/status"},
	)
	if err != nil {
		t.Fatalf("publish announcement: %v", err)
	}
	if delivered != 2 {
		t.Fatalf("expected two active recipients, got %d", delivered)
	}
	if len(pushed) != 2 {
		t.Fatalf("expected two notifications to publish after commit, got %d", len(pushed))
	}

	var notifications []model.Notification
	if err := db.Order("recipient_id").Find(&notifications).Error; err != nil {
		t.Fatalf("list announcement notifications: %v", err)
	}
	if len(notifications) != 2 {
		t.Fatalf("expected two persisted announcement notifications, got %d", len(notifications))
	}
	for _, notification := range notifications {
		if notification.Type != "site_announcement" || notification.SourceType != "site_announcement" {
			t.Fatalf("expected site announcement notification, got %#v", notification)
		}
		if notification.ActorID == nil || *notification.ActorID != admin.UUID {
			t.Fatalf("expected announcement actor %s, got %#v", admin.UUID, notification.ActorID)
		}
		if notification.Meta["title"] != "系统维护" || notification.Meta["body"] != "周日凌晨进行例行维护" || notification.Meta["path"] != "/status" {
			t.Fatalf("unexpected announcement metadata: %#v", notification.Meta)
		}
	}
}
