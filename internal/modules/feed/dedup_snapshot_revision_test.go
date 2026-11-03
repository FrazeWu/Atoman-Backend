package feed

import (
	"context"
	"errors"
	"testing"
	"time"

	"atoman/internal/model"
	"gorm.io/gorm"
)

func TestDedupSnapshotTrackedRevisionInvalidation(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	initial, err := repo.dedupSourceRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{FeedSourceID: source.ID, Title: "Initial unique headline", Link: "https://example.com/story/initial", GUID: "tracked-revision-initial", PublishedAt: time.Now().UTC()}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	inserted, err := repo.dedupSourceRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if inserted == initial {
		t.Fatalf("feed item insert did not invalidate revision %q", initial)
	}
	total, err := repo.publishPublicRecentDedupSnapshotTracked(ctx)
	if err != nil || total != 1 {
		t.Fatalf("tracked snapshot build: total=%d error=%v", total, err)
	}
	page, count, err := repo.readPublicRecentDedupSnapshotPageTracked(ctx, 1, 20)
	if err != nil || count != 1 || len(page) != 1 || page[0].ID != item.ID {
		t.Fatalf("tracked page: total=%d page=%+v err=%v", count, page, err)
	}

	// The content itself is hydrated live; full-text enrichment does not need
	// to invalidate the (title/link/date) deduplication projection.
	if err := db.Model(&item).Update("full_text_html", "<p>enriched content</p>").Error; err != nil {
		t.Fatal(err)
	}
	unchanged, err := repo.dedupSourceRevision(ctx)
	if err != nil || unchanged != inserted {
		t.Fatalf("full text unexpectedly invalidated snapshot: before=%q after=%q err=%v", inserted, unchanged, err)
	}
	page, _, err = repo.readPublicRecentDedupSnapshotPageTracked(ctx, 1, 20)
	if err != nil || page[0].FullTextHTML != "<p>enriched content</p>" {
		t.Fatalf("enriched text not hydrated: page=%+v err=%v", page, err)
	}

	// A rolled-back item update must not change the committed revision.
	expectedRollback := errors.New("rollback deliberately")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&item).Update("title", "uncommitted edit").Error; err != nil {
			return err
		}
		return expectedRollback
	}); !errors.Is(err, expectedRollback) {
		t.Fatalf("rollback: %v", err)
	}
	rolledBack, err := repo.dedupSourceRevision(ctx)
	if err != nil || rolledBack != inserted {
		t.Fatalf("rolled back change invalidated snapshot: %q, %v", rolledBack, err)
	}

	if err := db.Model(&item).Update("title", "Changed headline that alters grouping").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.readPublicRecentDedupSnapshotPageTracked(ctx, 1, 20); !errors.Is(err, ErrDedupSnapshotStale) {
		t.Fatalf("changed title must invalidate projection: %v", err)
	}
	if total, err := repo.publishPublicRecentDedupSnapshotTracked(ctx); err != nil || total != 1 {
		t.Fatalf("rebuild after title edit: total=%d err=%v", total, err)
	}
	page, count, err = repo.readPublicRecentDedupSnapshotPageTracked(ctx, 1, 20)
	if err != nil || count != 1 || len(page) != 1 || page[0].Title != "Changed headline that alters grouping" {
		t.Fatalf("fresh title mismatch: page=%+v err=%v", page, err)
	}

	if err := db.Model(&source).Update("hidden", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.readPublicRecentDedupSnapshotPageTracked(ctx, 1, 20); !errors.Is(err, ErrDedupSnapshotStale) {
		t.Fatalf("hidden source must invalidate projection: %v", err)
	}
	if total, err := repo.publishPublicRecentDedupSnapshotTracked(ctx); err != nil || total != 0 {
		t.Fatalf("rebuild after source hidden: total=%d err=%v", total, err)
	}
	page, count, err = repo.readPublicRecentDedupSnapshotPageTracked(ctx, 1, 20)
	if err != nil || count != 0 || len(page) != 0 {
		t.Fatalf("hidden source remains visible: page=%+v total=%d err=%v", page, count, err)
	}
}
