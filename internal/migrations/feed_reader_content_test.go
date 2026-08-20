package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunFeedReaderContentMigrationBackfillsLegacySuccess(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	source := model.FeedSource{SourceType: "external_rss", Hash: "reader-migration-source", RssURL: "https://example.com/feed.xml"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{
		FeedSourceID:   source.ID,
		GUID:           "legacy-success",
		FullTextStatus: "success",
		FullTextHTML:   "<p>legacy reader content</p>",
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	if err := RunFeedReaderContentMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&item, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.ReaderHTML != item.FullTextHTML || item.ReaderSource != "page" || item.ReaderVersion != 1 {
		t.Fatalf("backfilled item=%+v", item)
	}
	if !db.Migrator().HasIndex("feed_items", "idx_feed_items_reader_quality") {
		t.Fatal("reader quality index missing")
	}
}
