package feed

import (
	"context"
	"reflect"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
)

// Differential contract test: one supported slice of the future projection
// integration, external RSS-only and recent sorting with no user filters.
// Mixed blogs, search, popular/random, subscriptions and read flags are not
// covered and MUST NOT be routed to the projection without additional tests.
func TestPublicRecentDedupProjectionMatchesLegacyRSSOnly(t *testing.T) {
	service, db, _ := newFeedTestService(t)
	if err := db.Exec("DELETE FROM feed_item_reads").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM feed_items").Error; err != nil {
		t.Fatal(err)
	}
	clearFeedCanonicalBlogContent(t, db)
	if err := db.AutoMigrate(&feedDedupSnapshotHead{}, &feedDedupSnapshotGeneration{}, &feedDedupSnapshotRow{}); err != nil {
		t.Fatal(err)
	}
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	var first model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&first).Error; err != nil {
		t.Fatal(err)
	}
	second := model.FeedSource{SourceType: "external_rss", Hash: "feed-contract-second-source", Title: "Other RSS provider", RssURL: "https://contract.example/rss"}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	fixtures := []model.FeedItem{
		{FeedSourceID: first.ID, GUID: "contract-a", Title: "Cross source story", Link: "https://news.example/a", PublishedAt: base.Add(5 * time.Minute), FullTextHTML: "<p>source A full text</p>"},
		{FeedSourceID: second.ID, GUID: "contract-mirror-a", Title: "Cross source mirror", Link: "https://news.example/a?utm_source=mirror", PublishedAt: base.Add(4 * time.Minute)},
		{FeedSourceID: first.ID, GUID: "contract-b", Title: "Unrelated second headline", Link: "https://news.example/b", PublishedAt: base.Add(3 * time.Minute)},
		{FeedSourceID: second.ID, GUID: "contract-c", Title: "Independent third event", Link: "https://news.example/c", PublishedAt: base.Add(2 * time.Minute)},
	}
	for i := range fixtures {
		if err := db.Create(&fixtures[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if count, err := service.repo.publishPublicRecentDedupSnapshotTracked(context.Background()); err != nil || count != 3 {
		t.Fatalf("snapshot count=%d err=%v", count, err)
	}
	for page := 1; page <= 4; page++ {
		q := FeedQuery{Page: page, PageSize: 1, HideDuplicates: true}
		legacyPublic, totalPublic, err := service.GetPublicFeed(q)
		if err != nil {
			t.Fatal(err)
		}
		legacyExplore, totalExplore, err := service.GetExploreFeed(authctx.CurrentUser{}, q)
		if err != nil {
			t.Fatal(err)
		}
		projected, totalProjection, err := service.repo.readPublicRecentDedupSnapshotPageTracked(context.Background(), page, 1)
		if err != nil {
			t.Fatal(err)
		}
		if totalPublic != totalProjection || totalExplore != totalProjection {
			t.Fatalf("page %d count mismatch: public %d explore %d snapshot %d", page, totalPublic, totalExplore, totalProjection)
		}
		if len(legacyPublic) != len(projected) || len(legacyExplore) != len(projected) {
			t.Fatalf("page %d row count mismatch", page)
		}
		for i := range projected {
			if legacyPublic[i].FeedItem == nil || legacyExplore[i].FeedItem == nil {
				t.Fatalf("page %d contains unexpected non-RSS content", page)
			}
			want := projected[i]
			for _, item := range []*model.FeedItem{legacyPublic[i].FeedItem, legacyExplore[i].FeedItem} {
				if item.ID != want.ID || item.DuplicateCount != want.DuplicateCount || !reflect.DeepEqual(item.DuplicateItemIDs, want.DuplicateItemIDs) || !reflect.DeepEqual(item.DuplicateSources, want.DuplicateSources) {
					t.Fatalf("page %d projection differs from existing API for %s", page, want.ID)
				}
				if item.FullTextHTML != want.FullTextHTML {
					t.Fatalf("page %d full text differs", page)
				}
			}
		}
	}
}
