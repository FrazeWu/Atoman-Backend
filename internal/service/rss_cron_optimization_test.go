package service

import (
	"testing"
	"time"

	"atoman/internal/model"
)

func TestRSSFetchIntervalExpandsOnlyAfterUnchangedResponses(t *testing.T) {
	if got := rssFetchInterval(true, 0); got != 15*time.Minute {
		t.Fatalf("subscribed baseline=%s", got)
	}
	if got := rssFetchInterval(true, 3); got != 2*time.Hour {
		t.Fatalf("subscribed idle interval=%s", got)
	}
	if got := rssFetchInterval(false, 2); got != 24*time.Hour {
		t.Fatalf("unsubscribed idle interval=%s", got)
	}
}

func TestPersistParsedFeedItemsBatchesNewItemsAndUpdatesExistingItems(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	source := model.FeedSource{SourceType: "external_rss", Hash: "rss-batch-source", RssURL: "https://feeds.example.com/feed.xml", FullTextEnabled: true}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	items := []ExtRSSItem{
		{GUID: "first", Link: "https://feeds.example.com/posts/first", Title: "First", Description: "Original summary"},
		{GUID: "second", Link: "https://feeds.example.com/posts/second", Title: "Second", Description: "Second summary"},
	}
	inserted, err := persistParsedFeedItems(db, source, items, "Example", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 2 {
		t.Fatalf("inserted=%d, want 2", inserted)
	}

	items[0].Title = "First updated"
	items[0].Description = "Updated summary"
	inserted, err = persistParsedFeedItems(db, source, items, "Example", "", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 0 {
		t.Fatalf("updated batch inserted=%d, want 0", inserted)
	}

	var first model.FeedItem
	if err := db.Where("feed_source_id = ? AND guid = ?", source.ID, "first").First(&first).Error; err != nil {
		t.Fatal(err)
	}
	if first.Title != "First updated" || first.Summary == "Original summary" {
		t.Fatalf("existing item was not refreshed: %+v", first)
	}
}
