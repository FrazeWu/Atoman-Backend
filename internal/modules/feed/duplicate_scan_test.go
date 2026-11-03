package feed

import (
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
)

func TestDuplicateFeedTimelineHydratesOnlySelectedPages(t *testing.T) {
	service, db, user := newFeedTestService(t)
	if err := db.Exec("DELETE FROM feed_item_reads").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM feed_items").Error; err != nil {
		t.Fatal(err)
	}
	clearFeedCanonicalBlogContent(t, db)

	var firstSource model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&firstSource).Error; err != nil {
		t.Fatal(err)
	}
	secondSource := model.FeedSource{SourceType: "external_rss", Hash: "duplicate-page-hydration-second-source", Title: "Second source", RssURL: "https://second.example.com/feed.xml"}
	if err := db.Create(&secondSource).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Subscription{UserID: user.ID, FeedSourceID: secondSource.ID, Title: secondSource.Title}).Error; err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fullText := strings.Repeat("full text content ", 800)
	canonical := model.FeedItem{FeedSourceID: firstSource.ID, GUID: "heavy-primary", Link: "https://example.org/canonical", Title: "Heavy story", PublishedAt: base.Add(3 * time.Hour), FetchedAt: base, ReaderHTML: "<p>reader html</p>", FullTextHTML: fullText, Summary: "Original article summary"}
	mirror := model.FeedItem{FeedSourceID: secondSource.ID, GUID: "heavy-mirror", Link: "https://example.org/canonical?utm_source=feed", Title: "Heavy story mirror", PublishedAt: base.Add(2 * time.Hour), FetchedAt: base, ReaderHTML: strings.Repeat("mirror", 1000)}
	unique := model.FeedItem{FeedSourceID: firstSource.ID, GUID: "heavy-unique", Link: "https://example.org/unique", Title: "Unique story", PublishedAt: base.Add(time.Hour), FetchedAt: base, FullTextHTML: "<p>unique full text</p>"}
	for _, value := range []*model.FeedItem{&canonical, &mirror, &unique} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}

	metadata, err := service.repo.ListFeedItemsBySourceIDsForDuplicateScan([]uuid.UUID{firstSource.ID, secondSource.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 3 {
		t.Fatalf("want three metadata rows, got %d", len(metadata))
	}
	for _, row := range metadata {
		if row.FullTextHTML != "" || row.ReaderHTML != "" || row.Summary != "" {
			t.Fatalf("duplicate scan loaded a heavy field for %s", row.ID)
		}
		if row.FeedSource == nil {
			t.Fatalf("duplicate scan missed source metadata for %s", row.ID)
		}
	}
	exploreMeta, err := service.repo.ListExploreFeedItemsForDuplicateScan("recent", FeedQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(exploreMeta) != 3 || exploreMeta[0].FullTextHTML != "" || exploreMeta[0].ReaderHTML != "" {
		t.Fatalf("explore duplicate scan loaded a heavy row or wrong item count: %d", len(exploreMeta))
	}

	publicPage, total, err := service.GetPublicFeed(FeedQuery{Page: 1, PageSize: 1, HideDuplicates: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(publicPage) != 1 || publicPage[0].FeedItem == nil || publicPage[0].FeedItem.ID != canonical.ID {
		t.Fatalf("unexpected public duplicate page: total=%d items=%+v", total, publicPage)
	}
	if publicPage[0].FeedItem.FullTextHTML != fullText || publicPage[0].FeedItem.DuplicateCount != 2 || len(publicPage[0].FeedItem.DuplicateItemIDs) != 2 {
		t.Fatal("public page lost either original full text or duplicate metadata")
	}
	explorePage, total, err := service.GetExploreFeed(authctx.CurrentUser{}, FeedQuery{Page: 2, PageSize: 1, HideDuplicates: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(explorePage) != 1 || explorePage[0].FeedItem == nil || explorePage[0].FeedItem.ID != unique.ID || explorePage[0].FeedItem.FullTextHTML != unique.FullTextHTML {
		t.Fatalf("unexpected hydrated explore page: total=%d items=%+v", total, explorePage)
	}
	subscribed, total, err := service.GetSubscribedFeed(user, FeedQuery{SourceType: "external_rss", Page: 1, PageSize: 1, HideDuplicates: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(subscribed) != 1 || subscribed[0].FeedItem == nil || subscribed[0].FeedItem.FullTextHTML != fullText {
		t.Fatalf("unexpected hydrated subscribed page: total=%d items=%+v", total, subscribed)
	}
}

func TestReadMapLargeCandidateSet(t *testing.T) {
	service, db, user := newFeedTestService(t)
	var seed model.FeedItem
	if err := db.First(&seed).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.FeedItemRead{UserID: user.ID, FeedItemID: seed.ID, ReadAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	candidates := make([]model.FeedItem, 2048)
	candidates[0] = seed
	for i := 1; i < len(candidates); i++ {
		candidates[i].ID = uuid.New()
	}
	readMap, err := service.readMap(user.ID, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(readMap) != 1 || !readMap[seed.ID] {
		t.Fatalf("unexpected large-page read mapping: %v", readMap)
	}
}
