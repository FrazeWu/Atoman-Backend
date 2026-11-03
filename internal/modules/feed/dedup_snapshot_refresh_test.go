package feed

import (
	"context"
	"errors"
	"testing"
	"time"

	"atoman/internal/model"
)

func TestDedupRefreshDefersBatchesAndPublishesAtomicGeneration(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	recent := model.FeedItem{FeedSourceID: source.ID, Title: "Test item for refresh", Link: "https://refresh.example/first", GUID: "refresh-first", PublishedAt: time.Now().UTC()}
	if err := db.Create(&recent).Error; err != nil {
		t.Fatal(err)
	}
	quiet := PublicDedupRefreshPolicy{QuietPeriod: 10 * time.Minute}
	result, err := repo.refreshPublicRecentDedupIfQuiet(ctx, quiet)
	if err != nil || result != PublicDedupRefreshDeferred {
		t.Fatalf("expected quiet window deferral: state=%q err=%v", result, err)
	}
	if _, _, err := repo.readPublicRecentDedupSnapshotPageTracked(ctx, 1, 20); !errors.Is(err, ErrDedupSnapshotStale) {
		t.Fatalf("unbuilt snapshot must not be visible: %v", err)
	}
	result, err = repo.refreshPublicRecentDedupIfQuiet(ctx, PublicDedupRefreshPolicy{})
	if err != nil || result != PublicDedupRefreshRebuilt {
		t.Fatalf("expected rebuild: state=%q err=%v", result, err)
	}
	var firstHead feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&firstHead).Error; err != nil {
		t.Fatal(err)
	}
	result, err = repo.refreshPublicRecentDedupIfQuiet(ctx, PublicDedupRefreshPolicy{})
	if err != nil || result != PublicDedupRefreshCurrent {
		t.Fatalf("expected no-op current: state=%q err=%v", result, err)
	}
	var unchanged feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&unchanged).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.GenerationID != firstHead.GenerationID {
		t.Fatal("unchanged data triggered an unnecessary rebuild")
	}
	added := model.FeedItem{FeedSourceID: source.ID, Title: "Another independent headline", Link: "https://refresh.example/other", GUID: "refresh-other", PublishedAt: time.Now().UTC().Add(time.Minute)}
	if err := db.Create(&added).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.readPublicRecentDedupSnapshotPageTracked(ctx, 1, 20); !errors.Is(err, ErrDedupSnapshotStale) {
		t.Fatalf("new data must invalidate old generation: %v", err)
	}
	result, err = repo.refreshPublicRecentDedupIfQuiet(ctx, PublicDedupRefreshPolicy{})
	if err != nil || result != PublicDedupRefreshIncremental {
		t.Fatalf("expected safe newest prefix insert: state=%q err=%v", result, err)
	}
	var refreshed feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&refreshed).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.GenerationID != firstHead.GenerationID || refreshed.PositionBase != firstHead.PositionBase-1 {
		t.Fatalf("safe independent newest insert must preserve generation and move position base: before=%+v after=%+v", firstHead, refreshed)
	}
	rows, total, err := repo.readPublicRecentDedupSnapshotPageTracked(ctx, 1, 20)
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("safe refresh did not include new content: total=%d rows=%+v err=%v", total, rows, err)
	}
}

func TestDedupRefreshOneWorkerAtATime(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	item := model.FeedItem{FeedSourceID: source.ID, Title: "Single worker test", Link: "https://refresh.example/lock", GUID: "refresh-lock", PublishedAt: time.Now().UTC()}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1, $2)", dedupRefreshLockClass, dedupRefreshLockObject).Scan(&locked); err != nil || !locked {
		t.Fatalf("cannot hold rebuild lock: %v %v", locked, err)
	}
	defer func() {
		var released bool
		_ = conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1, $2)", dedupRefreshLockClass, dedupRefreshLockObject).Scan(&released)
	}()
	state, err := repo.refreshPublicRecentDedupIfQuiet(ctx, PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshBusy {
		t.Fatalf("second refresh should skip when locked: state=%q err=%v", state, err)
	}
}

func TestDedupRefreshRejectsUnsafePolicy(t *testing.T) {
	repo, _, _ := prepareDedupSnapshotTest(t)
	if _, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{QuietPeriod: -time.Second}); err == nil {
		t.Fatal("negative quiet period accepted")
	}
	if err := repo.RunPublicRecentDedupRefreshLoop(context.Background(), time.Millisecond, PublicDedupRefreshPolicy{}); err == nil {
		t.Fatal("too-frequent polling accepted")
	}
}
