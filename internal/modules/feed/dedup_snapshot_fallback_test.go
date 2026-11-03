package feed

import (
	"context"
	"errors"
	"testing"
	"time"

	"atoman/internal/model"
)

func TestDedupReadFallbackReturnsLegacyOnStale(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	first := model.FeedItem{FeedSourceID: source.ID, Title: "First fallback story", Link: "https://fallback.example/first", GUID: "fallback-first", PublishedAt: time.Now().UTC()}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.publishPublicRecentDedupSnapshotTracked(context.Background()); err != nil {
		t.Fatal(err)
	}
	called := 0
	legacy := func(_ context.Context, page, size int) ([]model.FeedItem, int64, error) {
		called++
		if page != 1 || size != 10 {
			t.Errorf("lost pagination arguments")
		}
		return []model.FeedItem{{Title: "legacy record"}}, 7, nil
	}
	var fallbackErr error
	items, total, err := repo.readPublicRecentDedupPageWithFallback(context.Background(), 1, 10, time.Second, legacy, func(err error) { fallbackErr = err })
	if err != nil || called != 0 || total != 1 || len(items) != 1 || items[0].ID != first.ID {
		t.Fatalf("healthy projection not preferred: items=%v total=%d err=%v called=%d", items, total, err, called)
	}
	if err := db.Model(&model.FeedItem{}).Where("id = ?", first.ID).Update("title", "updated").Error; err != nil {
		t.Fatal(err)
	}
	items, total, err = repo.readPublicRecentDedupPageWithFallback(context.Background(), 1, 10, time.Second, legacy, func(err error) { fallbackErr = err })
	if err != nil || called != 1 || total != 7 || len(items) != 1 || items[0].Title != "legacy record" {
		t.Fatalf("stale projection not degraded: items=%v total=%d err=%v called=%d", items, total, err, called)
	}
	if !errors.Is(fallbackErr, ErrDedupSnapshotStale) {
		t.Fatalf("fallback cause not observable: %v", fallbackErr)
	}
}

func TestDedupReadFallbackPropagatesBothFailuresAndCancellation(t *testing.T) {
	repo, _, _ := prepareDedupSnapshotTest(t)
	legacyErr := errors.New("legacy query failed")
	called := 0
	legacy := func(context.Context, int, int) ([]model.FeedItem, int64, error) {
		called++
		return nil, 0, legacyErr
	}
	_, _, err := repo.readPublicRecentDedupPageWithFallback(context.Background(), 1, 10, time.Second, legacy, nil)
	if !errors.Is(err, legacyErr) || called != 1 {
		t.Fatalf("fallback error swallowed: %v, calls=%d", err, called)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = repo.readPublicRecentDedupPageWithFallback(ctx, 1, 10, time.Second, legacy, nil)
	if !errors.Is(err, context.Canceled) || called != 1 {
		t.Fatalf("canceled request ran fallback: %v calls=%d", err, called)
	}
	_, _, err = repo.readPublicRecentDedupPageWithFallback(context.Background(), 1, 10, 0, legacy, nil)
	if err == nil {
		t.Fatal("zero projection timeout accepted")
	}
}
