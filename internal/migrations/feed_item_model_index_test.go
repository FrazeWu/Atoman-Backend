package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestFeedItemAutoMigrateDoesNotCreateUniqueIndexes(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})

	if db.Migrator().HasIndex("feed_items", "idx_feed_items_source_guid") {
		t.Fatal("expected AutoMigrate not to create idx_feed_items_source_guid")
	}
	if db.Migrator().HasIndex("feed_items", "idx_feed_items_source_link") {
		t.Fatal("expected AutoMigrate not to create idx_feed_items_source_link")
	}
}
