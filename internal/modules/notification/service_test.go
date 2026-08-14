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
