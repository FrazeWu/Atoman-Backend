package feed

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// copyCancelFixture loads an already-published GORM generation so that a
// canceled COPY can be compared against a real, readable baseline head.
func copyCancelFixture(t *testing.T) (*Repo, *gorm.DB, feedDedupSnapshotHead, []feedDedupSnapshotRow, []feedDedupSnapshotMember) {
	t.Helper()
	repo, db, source := prepareDedupSnapshotTest(t)
	snapshotCopyFixture(t, db, source)
	if _, err := repo.publishPublicRecentDedupSnapshot(context.Background(), "last-good-revision"); err != nil {
		t.Fatal(err)
	}
	var head feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&head).Error; err != nil {
		t.Fatal(err)
	}
	var rows []feedDedupSnapshotRow
	if err := db.Where("generation_id = ?", head.GenerationID).Order("position").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	var members []feedDedupSnapshotMember
	if err := db.Where("generation_id = ?", head.GenerationID).Order("feed_item_id").Find(&members).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || len(members) == 0 {
		t.Fatal("fixture generated no rows")
	}
	return repo, db, head, rows, members
}

func copyCancelAssertUnchanged(t *testing.T, repo *Repo, db *gorm.DB, old feedDedupSnapshotHead, failed uuid.UUID, nRows, nMembers int64) {
	t.Helper()
	var head feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&head).Error; err != nil {
		t.Fatal(err)
	}
	if head.GenerationID != old.GenerationID || head.Revision != old.Revision || head.Total != old.Total {
		t.Fatalf("canceled publish changed live head: %v", head)
	}
	for _, table := range []string{"feed_dedup_snapshot_generations", "feed_dedup_snapshot_rows", "feed_dedup_snapshot_members"} {
		var n int64
		if err := db.Raw("SELECT count(*) FROM "+table+" WHERE generation_id=?", failed).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("canceled publish leaked %d rows in %s", n, table)
		}
	}
	var nRowsNow, nMembersNow int64
	if err := db.Model(&feedDedupSnapshotRow{}).Where("generation_id=?", old.GenerationID).Count(&nRowsNow).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&feedDedupSnapshotMember{}).Where("generation_id=?", old.GenerationID).Count(&nMembersNow).Error; err != nil {
		t.Fatal(err)
	}
	if nRowsNow != nRows || nMembersNow != nMembers {
		t.Fatal("last-good generation incomplete after cancellation")
	}
	if _, _, err := repo.readPublicRecentDedupSnapshotPage(context.Background(), old.Revision, 1, 10); err != nil {
		t.Fatalf("previous snapshot not readable after cancel: %v", err)
	}
}

func TestSnapshotCopyContextCancellationRollback(t *testing.T) {
	repo, db, old, rows, members := copyCancelFixture(t)
	var nRows, nMembers int64
	nRows, nMembers = int64(len(rows)), int64(len(members))
	// Both stages happen inside the same pinned pgx transaction and AFTER
	// writing actual rows, not just before BEGIN. A canceled context must
	// never cause a partially visible head or leave orphaned generations.
	for _, stageName := range []string{"insert_representatives", "insert_members"} {
		t.Run(stageName, func(t *testing.T) {
			generation := uuid.New()
			stagedRows := append([]feedDedupSnapshotRow(nil), rows...)
			stagedMembers := append([]feedDedupSnapshotMember(nil), members...)
			for i := range stagedRows {
				stagedRows[i].GenerationID = generation
			}
			for i := range stagedMembers {
				stagedMembers[i].GenerationID = generation
			}
			head := old
			head.GenerationID = generation
			head.Revision = fmt.Sprintf("canceled-%s", stageName)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stageReached := false
			err := repo.publishDedupSnapshotViaCopyExperiment(ctx, generation, stagedRows, stagedMembers, head, func(name string, begin time.Time) {
				if name == stageName {
					stageReached = true
					cancel()
				}
			})
			if !stageReached {
				t.Fatal("COPY never reached the cancellation point")
			}
			if err == nil {
				t.Fatal("canceled COPY was committed")
			}
			if !errors.Is(err, context.Canceled) {
				t.Logf("cancellation surfaced driver error: %v", err)
			}
			copyCancelAssertUnchanged(t, repo, db, old, generation, nRows, nMembers)
		})
	}
	// A context canceled before opening a connection also must fail closed.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	rejected := uuid.New()
	head := old
	head.GenerationID = rejected
	err := repo.publishDedupSnapshotViaCopyExperiment(canceledCtx, rejected, nil, nil, head, func(string, time.Time) {})
	if err == nil {
		t.Fatal("pre-canceled context committed")
	}
	copyCancelAssertUnchanged(t, repo, db, old, rejected, nRows, nMembers)
	t.Logf("COPY_CANCEL_ROLLBACK stages=3 head_unchanged=true old_rows_intact=true no_orphans=true")
}

func TestSnapshotCopyConcurrentReaderBeforeCommit(t *testing.T) {
	repo, db, old, rows, members := copyCancelFixture(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(6)
	gen := uuid.New()
	for i := range rows {
		rows[i].GenerationID = gen
	}
	for i := range members {
		members[i].GenerationID = gen
	}
	head := old
	head.GenerationID = gen
	head.Revision = "concurrent-new-revision"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	blocked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	go func() {
		done <- repo.publishDedupSnapshotViaCopyExperiment(ctx, gen, rows, members, head, func(name string, begin time.Time) {
			if name == "insert_members" {
				close(blocked)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		})
	}()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatalf("COPY did not reach pre-commit gate: %v", ctx.Err())
	}
	// New rows have been copied but head upsert has NOT executed. Other
	// connections must continue reading only the old fully committed state.
	copyCancelAssertUnchanged(t, repo, db, old, gen, int64(len(rows)), int64(len(members)))
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("COPY failed after release: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("COPY commit blocked: %v", ctx.Err())
	}
	var visible feedDedupSnapshotHead
	if err := db.Where("scope=?", publicRecentDedupScope).Take(&visible).Error; err != nil {
		t.Fatal(err)
	}
	if visible.GenerationID != gen || visible.Revision != head.Revision {
		t.Fatalf("new committed head invisible: %v", visible)
	}
	if _, _, err := repo.readPublicRecentDedupSnapshotPage(context.Background(), head.Revision, 1, 10); err != nil {
		t.Fatal(err)
	}
	t.Logf("COPY_MVCC_READER prior_committed_visible=true uncommitted_generation_hidden=true atomic_swap=true")
}

func TestSnapshotCopyDeadlineExceededKeepsOldGeneration(t *testing.T) {
	repo, db, old, rows, members := copyCancelFixture(t)
	generation := uuid.New()
	for i := range rows {
		rows[i].GenerationID = generation
	}
	for i := range members {
		members[i].GenerationID = generation
	}
	head := old
	head.GenerationID = generation
	head.Revision = "must-not-publish-timeout"
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	stageReached := false
	err := repo.publishDedupSnapshotViaCopyExperiment(ctx, generation, rows, members, head, func(stage string, _ time.Time) {
		if stage == "insert_representatives" {
			stageReached = true
			<-ctx.Done()
		}
	})
	if !stageReached {
		t.Fatal("COPY not reached pre-members timeout checkpoint")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("deadline not reached: %v", ctx.Err())
	}
	if err == nil {
		t.Fatal("deadline-expired COPY committed")
	}
	copyCancelAssertUnchanged(t, repo, db, old, generation, int64(len(rows)), int64(len(members)))
	// Ensure a canceled connection does not poison the pool: recover using
	// the same Repo and database connection pool.
	recovered, err := repo.publishPublicRecentDedupSnapshot(context.Background(), "after-deadline-recovery")
	if err != nil || recovered != old.Total {
		t.Fatalf("pool failed to recover: count=%d err=%v", recovered, err)
	}
	t.Logf("COPY_TIMEOUT_ROLLBACK deadline=true no_orphans=true old_reader_ok=true pool_recovered=true")
}
