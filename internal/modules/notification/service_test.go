package notification

import (
	"testing"
	"time"

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
	if err := db.Model(&inactive).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate announcement user: %v", err)
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

func TestServiceListAnnouncementsGroupsRecipientsAndSupportsSearchPagination(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Notification{})
	admin := model.User{Username: "announcement-list-admin", Email: "announcement-list-admin@example.test", Password: "test", Role: authctx.RoleAdmin, IsActive: true, DisplayName: "站点管理员"}
	recipient := model.User{Username: "announcement-list-recipient", Email: "announcement-list-recipient@example.test", Password: "test", IsActive: true}
	other := model.User{Username: "announcement-list-other", Email: "announcement-list-other@example.test", Password: "test", IsActive: true}
	if err := db.Create([]*model.User{&admin, &recipient, &other}).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	firstSource := uuid.New()
	secondSource := uuid.New()
	firstAt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(time.Hour)
	if err := db.Create(&[]model.Notification{
		{RecipientID: recipient.UUID, ActorID: &admin.UUID, Type: announcementNotificationType, SourceType: announcementNotificationType, SourceID: firstSource, Meta: model.NotificationMeta{"title": "旧公告", "body": "旧内容", "path": "/status"}, Base: model.Base{CreatedAt: firstAt}},
		{RecipientID: other.UUID, ActorID: &admin.UUID, Type: announcementNotificationType, SourceType: announcementNotificationType, SourceID: firstSource, Meta: model.NotificationMeta{"title": "旧公告", "body": "旧内容", "path": "/status"}, Base: model.Base{CreatedAt: firstAt.Add(time.Minute)}},
		{RecipientID: recipient.UUID, ActorID: &admin.UUID, Type: announcementNotificationType, SourceType: announcementNotificationType, SourceID: secondSource, Meta: model.NotificationMeta{"title": "新公告", "body": "新内容", "path": ""}, Base: model.Base{CreatedAt: secondAt}},
	}).Error; err != nil {
		t.Fatalf("create announcements: %v", err)
	}

	items, total, err := NewService(db).ListAnnouncements(authctx.CurrentUser{ID: admin.UUID, Role: authctx.RoleAdmin}, ListAnnouncementsQuery{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("list announcements: %v", err)
	}
	if total != 2 || len(items) != 1 || items[0].Title != "新公告" || items[0].Delivered != 1 || items[0].Status != "delivered" {
		t.Fatalf("unexpected first page: total=%d items=%#v", total, items)
	}
	if items[0].Actor == nil || items[0].Actor.DisplayName != "站点管理员" {
		t.Fatalf("expected publisher details, got %#v", items[0].Actor)
	}

	items, total, err = NewService(db).ListAnnouncements(authctx.CurrentUser{ID: admin.UUID, Role: authctx.RoleAdmin}, ListAnnouncementsQuery{Search: "旧内容", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("search announcements: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].SourceID != firstSource.String() || items[0].Delivered != 2 {
		t.Fatalf("unexpected search result: total=%d items=%#v", total, items)
	}
}
