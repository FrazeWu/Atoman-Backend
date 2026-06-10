package main

import (
	"testing"

	"github.com/google/uuid"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestBackfillInternalRSSFeedSourcesConvertsRelativeURLs(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.FeedSource{})

	user := model.User{
		Username: "fazong",
		Email:    "fazong@example.com",
		Password: "hashed",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	source := model.FeedSource{
		SourceType: "external_rss",
		RssURL:     "/api/feed/rss/fazong",
		Hash:       uuid.NewString(),
		Title:      "fazong rss",
		Provider:   "rss",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	backfillInternalRSSFeedSources(db)

	var updated model.FeedSource
	if err := db.First(&updated, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("reload source: %v", err)
	}

	if updated.SourceType != "internal_user" {
		t.Fatalf("expected source_type internal_user, got %s", updated.SourceType)
	}
	if updated.SourceID == nil || *updated.SourceID != user.UUID {
		t.Fatalf("expected source_id %s, got %v", user.UUID, updated.SourceID)
	}
	if updated.RssURL != "" {
		t.Fatalf("expected rss_url cleared, got %q", updated.RssURL)
	}
}

func TestBackfillInternalRSSFeedSourcesMergesIntoExistingCanonicalSource(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.FeedSource{}, &model.Subscription{})

	user := model.User{
		Username: "fazong",
		Email:    "fazong@example.com",
		Password: "hashed",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	canonical := model.FeedSource{
		SourceType: "internal_user",
		SourceID:   &user.UUID,
		Hash:       buildInternalFeedSourceHash("internal_user", user.UUID),
		Provider:   "internal",
		Title:      "canonical",
	}
	if err := db.Create(&canonical).Error; err != nil {
		t.Fatalf("create canonical source: %v", err)
	}

	legacy := model.FeedSource{
		SourceType: "external_rss",
		RssURL:     "/api/feed/rss/fazong",
		Hash:       uuid.NewString(),
		Title:      "legacy rss",
		Provider:   "rss",
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("create legacy source: %v", err)
	}

	subscription := model.Subscription{
		UserID:       user.UUID,
		FeedSourceID: legacy.ID,
		Title:        "legacy sub",
	}
	if err := db.Create(&subscription).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	backfillInternalRSSFeedSources(db)

	var updatedSubscription model.Subscription
	if err := db.First(&updatedSubscription, "id = ?", subscription.ID).Error; err != nil {
		t.Fatalf("reload subscription: %v", err)
	}
	if updatedSubscription.FeedSourceID != canonical.ID {
		t.Fatalf("expected subscription feed_source_id %s, got %s", canonical.ID, updatedSubscription.FeedSourceID)
	}

	var legacyCount int64
	if err := db.Model(&model.FeedSource{}).Where("id = ?", legacy.ID).Count(&legacyCount).Error; err != nil {
		t.Fatalf("count legacy source: %v", err)
	}
	if legacyCount != 0 {
		t.Fatalf("expected legacy source to be removed, count=%d", legacyCount)
	}
}

func TestBackfillInternalRSSFeedSourcesSkipsUnknownUsers(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.FeedSource{})

	source := model.FeedSource{
		SourceType: "external_rss",
		RssURL:     "/api/feed/rss/missing-user",
		Hash:       uuid.NewString(),
		Title:      "missing user rss",
		Provider:   "rss",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	backfillInternalRSSFeedSources(db)

	var updated model.FeedSource
	if err := db.First(&updated, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("reload source: %v", err)
	}

	if updated.SourceType != "external_rss" {
		t.Fatalf("expected source_type external_rss, got %s", updated.SourceType)
	}
	if updated.RssURL != "/api/feed/rss/missing-user" {
		t.Fatalf("expected rss_url preserved, got %q", updated.RssURL)
	}
}
