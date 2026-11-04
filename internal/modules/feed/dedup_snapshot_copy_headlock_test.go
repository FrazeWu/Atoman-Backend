package feed

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"atoman/internal/model"
	"gorm.io/gorm/clause"
)

// A competing writer holds the live head row lock. A full COPY publication
// must respect the attempt deadline, roll back all uncommitted rows, and leave
// the previous published snapshot readable. Retrying after release must work.
func TestSnapshotCopyScopeHeadLockTimeoutAndRecovery(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	for i := 0; i < 40; i++ {
		record := model.FeedItem{FeedSourceID: source.ID, GUID: fmt.Sprintf("copy-head-lock-%02d", i), Title: fmt.Sprintf("Unrelated independent report %d", i), Link: fmt.Sprintf("https://locks.invalid/report/%d", i), PublishedAt: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Minute)}
		if err := db.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if _, err := repo.publishPublicRecentDedupSnapshot(ctx, "prior-head-lock"); err != nil {
		t.Fatal(err)
	}
	var before feedDedupSnapshotHead
	if err := db.Where("scope=?", publicRecentDedupScope).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	count := func(table string) int64 {
		var n int64
		if err := db.Raw("SELECT count(*) FROM " + table).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	tableNames := []string{"feed_dedup_snapshot_generations", "feed_dedup_snapshot_rows", "feed_dedup_snapshot_members"}
	counts := make([]int64, len(tableNames))
	for i, tbl := range tableNames {
		counts[i] = count(tbl)
	}
	held := db.Begin()
	if held.Error != nil {
		t.Fatal(held.Error)
	}
	defer held.Rollback()
	var locked feedDedupSnapshotHead
	if err := held.Clauses(clause.Locking{Strength: "UPDATE"}).Where("scope=?", publicRecentDedupScope).Take(&locked).Error; err != nil {
		t.Fatal(err)
	}
	if locked.GenerationID != before.GenerationID {
		t.Fatal("did not lock original live head")
	}
	t.Setenv("FEED_SCALE_ISOLATED", "yes")
	t.Setenv("FEED_SNAPSHOT_COPY_EXPERIMENT", "yes")
	deadlineCtx, cancel := context.WithTimeout(ctx, 650*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := repo.publishPublicRecentDedupSnapshot(deadlineCtx, "should-timeout-behind-lock")
	elapsed := time.Since(start)
	if err == nil || !errors.Is(deadlineCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("COPY ignored head lock/deadline: elapsed=%s err=%v context=%v", elapsed, err, deadlineCtx.Err())
	}
	var still feedDedupSnapshotHead
	if err := db.Where("scope=?", publicRecentDedupScope).Take(&still).Error; err != nil {
		t.Fatal(err)
	}
	if still.GenerationID != before.GenerationID || still.Revision != before.Revision {
		t.Fatalf("uncommitted COPY changed head: %+v", still)
	}
	for i, tbl := range tableNames {
		if got := count(tbl); got != counts[i] {
			t.Fatalf("failed COPY left partial %s: %d vs %d", tbl, got, counts[i])
		}
	}
	if _, _, err := repo.readPublicRecentDedupSnapshotPage(ctx, before.Revision, 1, 10); err != nil {
		t.Fatalf("old head unreadable during lock: %v", err)
	}
	if err := held.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	retryCtx, retryCancel := context.WithTimeout(ctx, 10*time.Second)
	defer retryCancel()
	_, err = repo.publishPublicRecentDedupSnapshot(retryCtx, "recovered-after-lock")
	if err != nil {
		t.Fatalf("COPY cannot publish after lock release: %v", err)
	}
	var final feedDedupSnapshotHead
	if err := db.Where("scope=?", publicRecentDedupScope).Take(&final).Error; err != nil {
		t.Fatal(err)
	}
	if final.GenerationID == before.GenerationID || final.Revision != "recovered-after-lock" {
		t.Fatalf("post-lock COPY head wrong: %+v", final)
	}
	t.Logf("COPY_HEAD_LOCK deadline_wait=%s error_on_timeout=true old_head_readable=true no_orphans=true retry_after_unlock=true", elapsed)
}
