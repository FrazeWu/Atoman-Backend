package service

import (
	"testing"
	"time"

	"atoman/internal/model"
)

func TestFullTextHostLeaseDefersWithoutConsumingAttempt(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 13, 0, 0, 0, time.UTC)

	lease, acquired, err := acquireFullTextHostLease(db, "https://example.com/posts/one", now)
	if err != nil || !acquired {
		t.Fatalf("first acquire acquired=%t err=%v", acquired, err)
	}
	if _, acquired, err := acquireFullTextHostLease(db, "https://example.com/posts/two", now); err != nil || acquired {
		t.Fatalf("second acquire acquired=%t err=%v", acquired, err)
	}

	source := model.FeedSource{SourceType: "external_rss", Hash: "host-lease-source", RssURL: "https://example.com/feed.xml", FullTextEnabled: true}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{FeedSourceID: source.ID, GUID: "deferred", Link: "https://example.com/posts/two", FullTextStatus: FullTextStatusFetching, FullTextAttemptCount: 2}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := deferFullTextHostClaim(db, item, now); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&item, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.FullTextStatus != FullTextStatusRetry || item.FullTextAttemptCount != 1 {
		t.Fatalf("deferred item=%+v", item)
	}
	if item.NextFullTextAttemptAt == nil || item.NextFullTextAttemptAt.Sub(now) != fullTextHostRetryDelay {
		t.Fatalf("next attempt=%v", item.NextFullTextAttemptAt)
	}

	if err := releaseFullTextHostLease(db, lease, now); err != nil {
		t.Fatal(err)
	}
	if _, acquired, err := acquireFullTextHostLease(db, "https://example.com/posts/three", now.Add(fullTextHostMinInterval)); err != nil || !acquired {
		t.Fatalf("acquire after release acquired=%t err=%v", acquired, err)
	}
}
