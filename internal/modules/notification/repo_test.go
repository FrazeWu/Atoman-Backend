package notification

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRepoListNotificationsFiltersKnownAndSystemCategories(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Notification{}, &model.NotificationPreference{}, &model.NotificationMute{})
	recipient := model.User{Username: "recipient", Email: "recipient@example.test", Password: "test", IsActive: true}
	if err := db.Create(&recipient).Error; err != nil {
		t.Fatalf("create notification recipient: %v", err)
	}
	recipientID := recipient.UUID
	if err := db.Create(&[]model.Notification{
		{RecipientID: recipientID, Type: "comment_reply", SourceType: "test", SourceID: uuid.New()},
		{RecipientID: recipientID, Type: "comment_like", SourceType: "test", SourceID: uuid.New()},
		{RecipientID: recipientID, Type: "future.notification", SourceType: "test", SourceID: uuid.New()},
	}).Error; err != nil {
		t.Fatalf("create notifications: %v", err)
	}
	repo := NewRepo(db)

	replies, replyTotal, err := repo.ListNotifications(recipientID, ListQuery{Category: "reply"})
	if err != nil {
		t.Fatalf("list replies: %v", err)
	}
	if replyTotal != 1 || len(replies) != 1 || replies[0].Type != "comment_reply" {
		t.Fatalf("expected only reply notification, got total=%d items=%#v", replyTotal, replies)
	}

	system, systemTotal, err := repo.ListNotifications(recipientID, ListQuery{Category: "system"})
	if err != nil {
		t.Fatalf("list system: %v", err)
	}
	if systemTotal != 1 || len(system) != 1 || system[0].Type != "future.notification" {
		t.Fatalf("expected only open-ended system notification, got total=%d items=%#v", systemTotal, system)
	}
}
