package subscription

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestCreateSubscriptionAllowsResubscribeAfterSoftDelete(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
	)

	service := NewService(db)
	user := model.User{Username: "alice_" + uuid.NewString()[:8], Email: uuid.NewString() + "@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	currentUser := authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser}
	req := CreateSubscriptionRequest{
		TargetType: "external_rss",
		RSSURL:     "https://example.com/feed.xml",
		Title:      "Example Feed",
	}

	created, err := service.CreateSubscription(currentUser, req)
	if err != nil {
		t.Fatalf("create initial subscription: %v", err)
	}

	if err := db.Delete(&model.Subscription{}, "id = ?", created.ID).Error; err != nil {
		t.Fatalf("soft delete subscription: %v", err)
	}

	resubscribed, err := service.CreateSubscription(currentUser, req)
	if err != nil {
		t.Fatalf("resubscribe after soft delete: %v", err)
	}

	if resubscribed.ID == created.ID {
		t.Fatalf("expected resubscribe to create a new row, got same id %s", resubscribed.ID)
	}

	var activeCount int64
	if err := db.Model(&model.Subscription{}).
		Where("user_id = ? AND feed_source_id = ?", user.UUID, created.FeedSourceID).
		Count(&activeCount).Error; err != nil {
		t.Fatalf("count active subscriptions: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("expected 1 active subscription, got %d", activeCount)
	}

	var totalCount int64
	if err := db.Unscoped().Model(&model.Subscription{}).
		Where("user_id = ? AND feed_source_id = ?", user.UUID, created.FeedSourceID).
		Count(&totalCount).Error; err != nil {
		t.Fatalf("count all subscriptions: %v", err)
	}
	if totalCount != 2 {
		t.Fatalf("expected 2 total subscriptions including soft-deleted history, got %d", totalCount)
	}
}

func TestSubscriptionGroupRejectsDuplicateUserName(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.SubscriptionGroup{},
	)

	user := model.User{Username: "alice_" + uuid.NewString()[:8], Email: uuid.NewString() + "@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	first := model.SubscriptionGroup{UserID: user.UUID, Name: defaultSubscriptionGroupName}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first group: %v", err)
	}

	duplicate := model.SubscriptionGroup{UserID: user.UUID, Name: defaultSubscriptionGroupName}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("expected duplicate subscription group to be rejected")
	}

	var count int64
	if err := db.Model(&model.SubscriptionGroup{}).
		Where("user_id = ? AND name = ?", user.UUID, defaultSubscriptionGroupName).
		Count(&count).Error; err != nil {
		t.Fatalf("count default groups: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 default group, got %d", count)
	}
}
