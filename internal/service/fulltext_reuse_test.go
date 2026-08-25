package service

import (
	"net"
	"testing"
	"time"

	"atoman/internal/model"
)

func TestCanonicalFullTextURLRemovesTrackingParameters(t *testing.T) {
	first := fullTextURLHash("HTTPS://Example.COM:443/article?b=2&utm_source=rss&a=1#section")
	second := fullTextURLHash("https://example.com/article?a=1&b=2&fbclid=ignored")
	if first == "" || first != second {
		t.Fatalf("tracking-normalized hashes differ: %q != %q", first, second)
	}
	if fullTextURLHash("https://example.com/article?a=1") == fullTextURLHash("https://example.com/article?a=2") {
		t.Fatal("meaningful query parameters must not be merged")
	}
}

func TestProcessFullTextItemReusesSuccessfulCanonicalURL(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	originalResolver := resolveFullTextHostname
	resolveFullTextHostname = func(host string) ([]net.IP, error) {
		if host != "example.com" {
			return nil, nil
		}
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	t.Cleanup(func() { resolveFullTextHostname = originalResolver })

	now := time.Date(2026, 8, 25, 11, 0, 0, 0, time.UTC)
	source := model.FeedSource{SourceType: "external_rss", Hash: "reuse-source", RssURL: "https://example.com/feed.xml", FullTextEnabled: true}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	cached := model.FeedItem{
		FeedSourceID:      source.ID,
		GUID:              "cached",
		Link:              "https://example.com/article?utm_source=feed",
		FullTextStatus:    FullTextStatusSuccess,
		FullTextURLHash:   fullTextURLHash("https://example.com/article"),
		FullTextHTML:      "<p>reused article body with enough content to pass reader checks.</p>",
		FullTextWordCount: 12,
	}
	if err := db.Create(&cached).Error; err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{FeedSourceID: source.ID, GUID: "new", Link: "https://example.com/article?fbclid=ignored", FullTextStatus: FullTextStatusFetching, FullTextAttemptCount: 1}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	if err := processFullTextItem(db, &item, &source, now); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&item, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.FullTextStatus != FullTextStatusSuccess || item.FullTextHTML != cached.FullTextHTML {
		t.Fatalf("reused item=%+v", item)
	}
	if item.FullTextURLHash != cached.FullTextURLHash {
		t.Fatalf("url hash=%q, want %q", item.FullTextURLHash, cached.FullTextURLHash)
	}

	var diagnostic model.FeedSourceDiagnostic
	if err := db.Where("feed_item_id = ? AND kind = ?", item.ID, "reused").First(&diagnostic).Error; err != nil {
		t.Fatalf("expected reuse diagnostic: %v", err)
	}
}
