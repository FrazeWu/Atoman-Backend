package service_test

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/service"
	"atoman/internal/testdb"
	"gorm.io/gorm"
)

func setupOwnerBootstrapTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.UserSettings{},
		&model.Channel{},
		&model.Collection{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
		&model.BookmarkFolder{},
	)
	return db
}

func TestEnsureOwnerCreatesDefaultResources(t *testing.T) {
	db := setupOwnerBootstrapTestDB(t)

	owner, created, err := service.NewOwnerBootstrapService(db).EnsureOwner(service.OwnerBootstrapInput{
		Username: "owner",
		Email:    "owner@example.com",
		Password: "change-me",
	})
	if err != nil {
		t.Fatalf("EnsureOwner error = %v", err)
	}
	if !created {
		t.Fatalf("EnsureOwner created = false, want true")
	}

	var channel model.Channel
	if err := db.Where("user_id = ? AND is_default = ?", owner.UUID, true).First(&channel).Error; err != nil {
		t.Fatalf("find default channel: %v", err)
	}

	var collection model.Collection
	if err := db.Where("channel_id = ? AND is_default = ?", channel.ID, true).First(&collection).Error; err != nil {
		t.Fatalf("find default collection: %v", err)
	}

	var group model.SubscriptionGroup
	if err := db.Where("user_id = ? AND name = ?", owner.UUID, "默认分组").First(&group).Error; err != nil {
		t.Fatalf("find default subscription group: %v", err)
	}

	var source model.FeedSource
	if err := db.Where("source_type = ? AND source_id = ?", "internal_user", owner.UUID).First(&source).Error; err != nil {
		t.Fatalf("find internal user feed source: %v", err)
	}

	var subscription model.Subscription
	if err := db.Where("user_id = ? AND feed_source_id = ?", owner.UUID, source.ID).First(&subscription).Error; err != nil {
		t.Fatalf("find self subscription: %v", err)
	}

	var folder model.BookmarkFolder
	if err := db.Where("user_id = ? AND name = ?", owner.UUID, "默认收藏").First(&folder).Error; err != nil {
		t.Fatalf("find default bookmark folder: %v", err)
	}
}
