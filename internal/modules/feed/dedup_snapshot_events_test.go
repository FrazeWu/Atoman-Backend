package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestDedupSourceChangeEventsCommitAtomicallyAndCoverBatch(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	batch := make([]model.FeedItem, 120)
	for i := range batch {
		batch[i] = model.FeedItem{FeedSourceID: source.ID, GUID: fmt.Sprintf("events-%d", i), Title: fmt.Sprintf("Unrelated event title %d", i), Link: fmt.Sprintf("https://events.example/%d", i), PublishedAt: time.Now().UTC()}
	}
	if err := db.CreateInBatches(&batch, 120).Error; err != nil {
		t.Fatal(err)
	}
	events, err := repo.dedupSourceEventsAfter(ctx, source.ID, 0)
	if err != nil || len(events) != 1 || events[0].Revision != 1 || events[0].Kind != "feed_items_insert" {
		t.Fatalf("batch event mismatch: %+v %v", events, err)
	}
	var ids []uuid.UUID
	if err := json.Unmarshal(events[0].ItemIDs, &ids); err != nil || len(ids) != 120 {
		t.Fatalf("expected 120 changed IDs in one event, got %d %v", len(ids), err)
	}
	if err := db.Model(&model.FeedItem{}).Where("feed_source_id = ?", source.ID).Update("full_text_html", "enriched content").Error; err != nil {
		t.Fatal(err)
	}
	events, err = repo.dedupSourceEventsAfter(ctx, source.ID, 1)
	if err != nil || len(events) != 0 {
		t.Fatalf("non-dedup update emitted journal events: events=%v err=%v", events, err)
	}
	expectedRollback := errors.New("discard test update")
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.FeedItem{}).Where("feed_source_id = ?", source.ID).Update("author", "not committed").Error; err != nil {
			return err
		}
		return expectedRollback
	})
	if !errors.Is(err, expectedRollback) {
		t.Fatal(err)
	}
	events, err = repo.dedupSourceEventsAfter(ctx, source.ID, 1)
	if err != nil || len(events) != 0 {
		t.Fatalf("rolled-back change emitted journal events: events=%v err=%v", events, err)
	}
	if err := db.Model(&model.FeedItem{}).Where("feed_source_id = ?", source.ID).Update("author", "committed").Error; err != nil {
		t.Fatal(err)
	}
	events, err = repo.dedupSourceEventsAfter(ctx, source.ID, 1)
	if err != nil || len(events) != 1 || events[0].Revision != 2 || events[0].Kind != "feed_items_update" {
		t.Fatalf("update journal mismatch: %+v %v", events, err)
	}
	if err := json.Unmarshal(events[0].ItemIDs, &ids); err != nil || len(ids) != 120 {
		t.Fatalf("update IDs mismatch: %d %v", len(ids), err)
	}
	if err := db.Model(&source).Update("hidden", true).Error; err != nil {
		t.Fatal(err)
	}
	events, err = repo.dedupSourceEventsAfter(ctx, source.ID, 2)
	if err != nil || len(events) != 1 || events[0].Kind != "feed_sources_update" {
		t.Fatalf("source hide not journaled: %+v %v", events, err)
	}
	if err := json.Unmarshal(events[0].ItemIDs, &ids); err != nil || len(ids) != 0 {
		t.Fatalf("source change should have no item IDs: %+v %v", ids, err)
	}
}

func TestDedupSourceJournalTracksMovedItemInBothSources(t *testing.T) {
	repo, db, sourceA := prepareDedupSnapshotTest(t)
	sourceB := model.FeedSource{SourceType: "external_rss", Title: "Other source", Hash: "dedup-journal-moved-item", RssURL: "https://journal.example/rss"}
	if err := db.Create(&sourceB).Error; err != nil {
		t.Fatal(err)
	}
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{FeedSourceID: sourceA.ID, Title: "Moved article", GUID: "journal-move", Link: "https://move.example/a", PublishedAt: time.Now().UTC()}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&item).Update("feed_source_id", sourceB.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   uuid.UUID
		want int64
	}{{sourceA.ID, 2}, {sourceB.ID, 1}} {
		events, err := repo.dedupSourceEventsAfter(context.Background(), tc.id, 0)
		if err != nil || len(events) != int(tc.want) {
			t.Fatalf("source %v journal length: %d, err %v", tc.id, len(events), err)
		}
		last := events[len(events)-1]
		if last.Kind != "feed_items_update" {
			t.Fatalf("source %v should journal move: %s", tc.id, last.Kind)
		}
		var ids []uuid.UUID
		if err := json.Unmarshal(last.ItemIDs, &ids); err != nil || len(ids) != 1 || ids[0] != item.ID {
			t.Fatalf("source %v move lost item id: %+v %v", tc.id, ids, err)
		}
	}
}

func TestDedupJournalTruncateInvalidatesSnapshot(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	item := model.FeedItem{FeedSourceID: source.ID, Title: "Truncate invalidates old feed projection", GUID: "truncate-event-1", Link: "https://truncate.example/post", PublishedAt: time.Now().UTC()}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.publishPublicRecentDedupSnapshotTracked(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("TRUNCATE TABLE feed_items CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.readPublicRecentDedupSnapshotPageTracked(ctx, 1, 20); !errors.Is(err, ErrDedupSnapshotStale) {
		t.Fatalf("truncation did not invalidate snapshot: %v", err)
	}
	events, err := repo.dedupSourceEventsAfter(ctx, uuid.Nil, 0)
	if err != nil || len(events) != 1 || events[0].Kind != "feed_items_truncate" {
		t.Fatalf("missing truncate sentinel event: %+v, %v", events, err)
	}
}
