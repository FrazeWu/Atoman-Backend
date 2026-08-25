package service

import (
	"testing"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func TestClaimFullTextBatchClaimsOneItemPerSourceAndLeasesSource(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	sourceA := model.FeedSource{SourceType: "external_rss", Hash: "claim-batch-source-a", RssURL: "https://a.example/feed.xml", FullTextEnabled: true}
	sourceB := model.FeedSource{SourceType: "external_rss", Hash: "claim-batch-source-b", RssURL: "https://b.example/feed.xml", FullTextEnabled: true}
	if err := db.Create(&sourceA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&sourceB).Error; err != nil {
		t.Fatal(err)
	}
	items := []model.FeedItem{
		{FeedSourceID: sourceA.ID, GUID: "a-1", Link: "https://a.example/posts/1", FullTextStatus: FullTextStatusPending},
		{FeedSourceID: sourceA.ID, GUID: "a-2", Link: "https://a.example/posts/2", FullTextStatus: FullTextStatusPending},
		{FeedSourceID: sourceB.ID, GUID: "b-1", Link: "https://b.example/posts/1", FullTextStatus: FullTextStatusPending},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}

	claimed, err := claimFullTextBatch(db, now, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed=%d, want 2", len(claimed))
	}
	seenSources := map[uuid.UUID]bool{}
	for _, claim := range claimed {
		if seenSources[claim.source.ID] {
			t.Fatalf("source %s was claimed more than once", claim.source.ID)
		}
		seenSources[claim.source.ID] = true
		if claim.leaseToken == "" || claim.item.FullTextStatus != FullTextStatusFetching {
			t.Fatalf("unexpected claim: %+v", claim)
		}
	}

	var leasedSources []model.FeedSource
	if err := db.Where("full_text_lease_until > ?", now).Find(&leasedSources).Error; err != nil {
		t.Fatal(err)
	}
	if len(leasedSources) != 2 {
		t.Fatalf("leased sources=%d, want 2", len(leasedSources))
	}
	claimedAgain, err := claimFullTextBatch(db, now, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimedAgain) != 0 {
		t.Fatalf("claimed while source leases are active: %d", len(claimedAgain))
	}
}
