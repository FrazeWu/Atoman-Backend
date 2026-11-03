package feed

import (
	"context"
	"testing"
	"time"

	"atoman/internal/model"
)

func TestDedupJournalPrunePreservesUncheckpointedChanges(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{FeedSourceID: source.ID, GUID: "journal-prune", Title: "Journal GC first title", Link: "https://gc.example.org/a", PublishedAt: time.Now().UTC()}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.publishPublicRecentDedupSnapshotTracked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.FeedItem{}).Where("id = ?", item.ID).Update("title", "Journal GC updated title").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&feedDedupChangeEvent{}).Where("feed_source_id = ?", source.ID).Update("created_at", time.Now().UTC().Add(-72*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	removed, err := repo.pruneDedupChangeEventsBeforeCheckpoint(context.Background(), 24*time.Hour)
	if err != nil || removed != 1 {
		t.Fatalf("must prune only events at or below committed snapshot: removed=%d err=%v", removed, err)
	}
	remaining, err := repo.dedupSourceEventsAfter(context.Background(), source.ID, 0)
	if err != nil || len(remaining) != 1 || remaining[0].Revision != 2 {
		t.Fatalf("unreplayed change was deleted: %+v err=%v", remaining, err)
	}
	if state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{}); err != nil || state != PublicDedupRefreshRebuilt {
		t.Fatalf("rebuild after journal pruning failed: %s %v", state, err)
	}
	removed, err = repo.pruneDedupChangeEventsBeforeCheckpoint(context.Background(), 24*time.Hour)
	if err != nil || removed != 1 {
		t.Fatalf("advanced checkpoint did not permit old journal pruning: %d %v", removed, err)
	}
}

func TestDedupJournalPruneRequiresActiveCheckpointAndGrace(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{FeedSourceID: source.ID, GUID: "journal-prune-unbuilt", Title: "Unbuilt journal", Link: "https://gc.example.org/b", PublishedAt: time.Now().UTC()}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pruneDedupChangeEventsBeforeCheckpoint(context.Background(), time.Hour); err == nil {
		t.Fatal("accepted short journal retention")
	}
	if err := db.Model(&feedDedupChangeEvent{}).Where("feed_source_id = ?", source.ID).Update("created_at", time.Now().UTC().Add(-72*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	removed, err := repo.pruneDedupChangeEventsBeforeCheckpoint(context.Background(), 24*time.Hour)
	if err != nil || removed != 0 {
		t.Fatalf("no checkpoint must never discard events: removed=%d err=%v", removed, err)
	}
	events, err := repo.dedupSourceEventsAfter(context.Background(), source.ID, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("untracked event was discarded: count=%d err=%v", len(events), err)
	}
}
