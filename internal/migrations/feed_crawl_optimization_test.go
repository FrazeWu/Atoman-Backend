package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunFeedCrawlOptimizationMigrationCreatesCoordinationColumnsAndIndexes(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})

	if err := RunFeedCrawlOptimizationMigration(db); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"fetch_unchanged_count", "full_text_lease_token", "full_text_lease_until"} {
		if !db.Migrator().HasColumn("feed_sources", column) {
			t.Fatalf("feed_sources.%s was not created", column)
		}
	}
	if !db.Migrator().HasTable(&model.FeedFullTextHost{}) {
		t.Fatal("feed_fulltext_hosts was not created")
	}
	if !db.Migrator().HasColumn("feed_items", "full_text_url_hash") {
		t.Fatal("feed_items.full_text_url_hash was not created")
	}
	if !db.Migrator().HasIndex("feed_sources", "idx_feed_sources_fulltext_lease") {
		t.Fatal("expected idx_feed_sources_fulltext_lease")
	}
	if !db.Migrator().HasIndex("feed_fulltext_hosts", "idx_feed_fulltext_hosts_ready") {
		t.Fatal("expected idx_feed_fulltext_hosts_ready")
	}
	if !db.Migrator().HasIndex("feed_items", "idx_feed_items_fulltext_url_hash") {
		t.Fatal("expected idx_feed_items_fulltext_url_hash")
	}
}
