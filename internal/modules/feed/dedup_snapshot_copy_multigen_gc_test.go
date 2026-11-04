package feed

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
)

// Synthetic, bounded multi-generation endurance and storage test. The producer
// and reader use an isolated test DB, never production. A reader pinned to an
// old generation must finish after GORM GC deletes inactive generations.
func TestSnapshotCopyMultiGenerationGCAndWAL(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const itemCount = 5000
	base := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	fixtures := make([]model.FeedItem, 0, itemCount)
	for i := 0; i < itemCount; i++ {
		fixtures = append(fixtures, model.FeedItem{
			FeedSourceID: source.ID, GUID: fmt.Sprintf("copy-endurance-guid-%05d", i),
			Link:        fmt.Sprintf("https://synthetic.invalid/endurance/%05d", i),
			Title:       fmt.Sprintf("Independent security report %s %d", uuid.NewString(), i),
			PublishedAt: base.Add(-time.Duration(i) * time.Second),
		})
	}
	if err := db.CreateInBatches(&fixtures, 125).Error; err != nil {
		t.Fatal(err)
	}
	var firstLSN string
	if err := db.Raw("SELECT pg_current_wal_insert_lsn()::text").Scan(&firstLSN).Error; err != nil {
		t.Fatal(err)
	}
	generations := make([]feedDedupSnapshotHead, 0, 7)
	publish := func(revision string) {
		t.Helper()
		n, err := repo.publishPublicRecentDedupSnapshot(ctx, revision)
		if err != nil || n != itemCount {
			t.Fatalf("publish %s: count=%d err=%v", revision, n, err)
		}
		var head feedDedupSnapshotHead
		if err := db.WithContext(ctx).Where("scope=?", publicRecentDedupScope).Take(&head).Error; err != nil {
			t.Fatal(err)
		}
		if head.Revision != revision || head.RawItemCount != itemCount || head.Total != itemCount {
			t.Fatalf("head metadata mismatch: %+v", head)
		}
		if len(generations) > 0 && generations[len(generations)-1].GenerationID == head.GenerationID {
			t.Fatal("generation pointer did not advance")
		}
		generations = append(generations, head)
	}
	// First generation via the unchanged GORM default, the others via opt-in
	// COPY to prove compatibility of generations and the standard GORM GC.
	publish("endurance-gorm-0")
	t.Setenv("FEED_SCALE_ISOLATED", "yes")
	t.Setenv("FEED_SNAPSHOT_COPY_EXPERIMENT", "yes")
	for i := 1; i <= 3; i++ {
		publish(fmt.Sprintf("endurance-copy-%d", i))
	}
	// Establish a long-running REPEATABLE READ view on COPY generation 3,
	// then publish three newer generations while the reader remains open.
	readerTx := db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if readerTx.Error != nil {
		t.Fatal(readerTx.Error)
	}
	defer readerTx.Rollback()
	var oldHead feedDedupSnapshotHead
	if err := readerTx.Where("scope=?", publicRecentDedupScope).Take(&oldHead).Error; err != nil {
		t.Fatal(err)
	}
	if oldHead.GenerationID != generations[3].GenerationID {
		t.Fatal("reader MVCC snapshot not pinned to COPY generation")
	}
	for i := 4; i <= 6; i++ {
		publish(fmt.Sprintf("endurance-copy-%d", i))
	}
	// All prior generations are test-only; fast forward their metadata into
	// GC eligibility instead of waiting an hour on a live test server.
	for _, gen := range generations[:len(generations)-1] {
		if err := db.Model(&feedDedupSnapshotGeneration{}).Where("generation_id=?", gen.GenerationID).
			Update("built_at", time.Now().UTC().Add(-2*time.Hour)).Error; err != nil {
			t.Fatal(err)
		}
	}
	var preMembers int64
	if err := db.Model(&feedDedupSnapshotMember{}).Count(&preMembers).Error; err != nil {
		t.Fatal(err)
	}
	if preMembers != int64(itemCount*len(generations)) {
		t.Fatalf("members before GC=%d", preMembers)
	}
	var dbBytesBefore int64
	if err := db.Raw("SELECT pg_database_size(current_database())").Scan(&dbBytesBefore).Error; err != nil {
		t.Fatal(err)
	}
	removed, err := repo.pruneInactiveDedupSnapshotGenerations(ctx, time.Hour)
	if err != nil || removed != int64(len(generations)-1) {
		t.Fatalf("old generations pruned=%d err=%v", removed, err)
	}
	// Old reader's MVCC snapshot was acquired BEFORE the new head and DELETE;
	// the reader must still see every old row until it commits.
	reader := &Repo{db: readerTx}
	page, total, err := reader.readPublicRecentDedupSnapshotPage(ctx, oldHead.Revision, 500, 10)
	if err != nil || total != itemCount || len(page) != 10 {
		t.Fatalf("MVCC reader broke after old COPY generation GC: total=%d rows=%d err=%v", total, len(page), err)
	}
	if err := readerTx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	var gens, rows, members int64
	if err := db.Model(&feedDedupSnapshotGeneration{}).Count(&gens).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&feedDedupSnapshotRow{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&feedDedupSnapshotMember{}).Count(&members).Error; err != nil {
		t.Fatal(err)
	}
	if gens != 1 || rows != itemCount || members != itemCount {
		t.Fatalf("GC retained wrong number of generations/rows/members: %d/%d/%d", gens, rows, members)
	}
	active := generations[len(generations)-1]
	_, total, err = repo.readPublicRecentDedupSnapshotPage(ctx, active.Revision, 1, 10)
	if err != nil || total != itemCount {
		t.Fatalf("active COPY head missing: %d %v", total, err)
	}
	again, err := repo.pruneInactiveDedupSnapshotGenerations(ctx, time.Hour)
	if err != nil || again != 0 {
		t.Fatalf("repeated GC not idempotent: %d %v", again, err)
	}
	var walBytes float64
	if err := db.Raw("SELECT pg_wal_lsn_diff(pg_current_wal_insert_lsn(), ?::pg_lsn)", firstLSN).Scan(&walBytes).Error; err != nil {
		t.Fatal(err)
	}
	var dbBytesAfter int64
	if err := db.Raw("SELECT pg_database_size(current_database())").Scan(&dbBytesAfter).Error; err != nil {
		t.Fatal(err)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	// DELETE does not necessarily reduce on-disk size before vacuum. Report
	// both numbers, but never assert that pg_database_size shrinks on GC.
	t.Logf("COPY_MULTIGEN_GC items=%d total_generations=%d deleted_generations=%d old_reader_survived=true active_only=true remaining_members=%d wal_mib=%.1f db_before_mib=%.1f db_after_mib=%.1f heap_mib=%.1f", itemCount, len(generations), removed, members, walBytes/(1024*1024), float64(dbBytesBefore)/(1024*1024), float64(dbBytesAfter)/(1024*1024), float64(mem.HeapAlloc)/(1024*1024))
}
