package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/google/uuid"
)

func TestRunFeedItemUniqueIndexCreatesExpectedIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})

	if err := RunFeedItemUniqueIndex(db); err != nil {
		t.Fatalf("run feed item unique index migration: %v", err)
	}

	assertIndexExists(t, db, "feed_items", "idx_feed_items_source_guid")
}

func TestRunFeedItemUniqueIndexDeduplicatesExistingRows(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	if err := db.Exec(`DROP INDEX IF EXISTS idx_feed_items_source_guid`).Error; err != nil {
		t.Fatalf("drop preexisting unique index: %v", err)
	}

	source := model.FeedSource{
		Base:       model.Base{ID: uuid.MustParse("018f0f58-31d2-7e35-bf72-111111111111")},
		SourceType: "external_rss",
		Hash:       "feed-item-dedupe-source",
		RssURL:     "https://example.com/feed.xml",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	oldID := uuid.MustParse("018f0f58-31d2-7e35-bf72-222222222222")
	newID := uuid.MustParse("018f0f58-31d2-7e35-bf72-333333333333")
	oldCreatedAt := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	newCreatedAt := oldCreatedAt.Add(time.Hour)

	if err := db.Exec(`
INSERT INTO feed_items (
	id, created_at, updated_at, feed_source_id, guid, title, link, published_at, fetched_at, full_text_status, full_text_attempt_count, full_text_word_count
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		oldID, oldCreatedAt, oldCreatedAt, source.ID, "post-1", "Older", "https://example.com/old", oldCreatedAt, oldCreatedAt, "disabled", 0, 0,
		newID, newCreatedAt, newCreatedAt, source.ID, "post-1", "Newer", "https://example.com/new", newCreatedAt, newCreatedAt, "disabled", 0, 0,
	).Error; err != nil {
		t.Fatalf("seed duplicate feed items: %v", err)
	}

	if err := RunFeedItemUniqueIndex(db); err != nil {
		t.Fatalf("run feed item unique index migration: %v", err)
	}

	var items []model.FeedItem
	if err := db.Order("created_at ASC").Find(&items).Error; err != nil {
		t.Fatalf("load feed items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d", len(items))
	}
	if items[0].ID != newID {
		t.Fatalf("expected newest duplicate retained, got %s", items[0].ID)
	}

	assertIndexExists(t, db, "feed_items", "idx_feed_items_source_guid")
}
