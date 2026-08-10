package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunFeedRecommendationIndexesCreatesIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	if err := RunFeedRecommendationIndexes(db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasIndex("feed_items", "idx_feed_items_recommendation_published") {
		t.Fatal("expected feed recommendation published index")
	}
}
