package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRunFeedSubscriptionManagementMigrationBackfillsBlankPriority(t *testing.T) {
	db := testdb.Open(t)
	if err := db.AutoMigrate(&model.User{}, &model.FeedSource{}, &model.Subscription{}, &model.SubscriptionGroup{}); err != nil {
		t.Fatalf("migrate subscriptions: %v", err)
	}
	user := model.User{UUID: uuid.New(), Username: "priority-owner", Email: "priority-owner@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	source := model.FeedSource{SourceType: "external_rss", Provider: "rss", Category: "blog", Hash: "priority-migration-source"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create feed source: %v", err)
	}

	subscription := model.Subscription{UserID: user.UUID, FeedSourceID: source.ID, Priority: "high"}
	if err := db.Create(&subscription).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	if err := db.Model(&model.Subscription{}).Where("id = ?", subscription.ID).Update("priority", "").Error; err != nil {
		t.Fatalf("create legacy blank priority: %v", err)
	}

	if err := RunFeedSubscriptionManagementMigration(db); err != nil {
		t.Fatalf("run subscription management migration: %v", err)
	}

	var updated model.Subscription
	if err := db.First(&updated, "id = ?", subscription.ID).Error; err != nil {
		t.Fatalf("reload subscription: %v", err)
	}
	if updated.Priority != "normal" {
		t.Fatalf("expected blank priority to become normal, got %q", updated.Priority)
	}
}
