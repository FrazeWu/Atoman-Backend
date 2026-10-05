package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunFeedRecommendationIndexesCreatesIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{}, &model.Subscription{})
	if err := RunFeedRecommendationIndexes(db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasIndex("feed_items", "idx_feed_items_recommendation_published") {
		t.Fatal("expected feed recommendation published index")
	}
	if !db.Migrator().HasIndex("feed_items", "idx_feed_items_recommendation_source_language_published") {
		t.Fatal("expected feed recommendation source language published index")
	}
	if !db.Migrator().HasIndex("feed_sources", "idx_feed_sources_curated_title") {
		t.Fatal("expected curated feed source title index")
	}
	if !db.Migrator().HasIndex("subscriptions", "idx_subscriptions_source_live") {
		t.Fatal("expected subscription source index")
	}
}
