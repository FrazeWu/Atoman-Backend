package feed

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
)

// Run only with FEED_SNAPSHOT_BENCHMARK=10000 (or 100000) and the
// scripts/test-isolated.sh runner. This never targets a production database.
func TestDedupSnapshotMeasuredBenchmark(t *testing.T) {
	raw := os.Getenv("FEED_SNAPSHOT_BENCHMARK")
	if raw == "" {
		t.Skip("requires explicit FEED_SNAPSHOT_BENCHMARK")
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 1 || count > 100000 {
		t.Fatalf("invalid benchmark size %q", raw)
	}
	repo, db, source := prepareDedupSnapshotTest(t)
	base := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	seed := make([]model.FeedItem, count)
	for i := range seed {
		seed[i] = model.FeedItem{FeedSourceID: source.ID, GUID: fmt.Sprintf("snapshot-metric-%06d", i),
			Title: uuid.NewString(), Link: fmt.Sprintf("https://metric.example.com/article/%d", i),
			PublishedAt: base.Add(-time.Duration(i) * time.Second)}
	}
	loadStart := time.Now()
	if err := db.CreateInBatches(seed, 250).Error; err != nil {
		t.Fatal(err)
	}
	seed = nil
	t.Logf("seed %d feed items: %s", count, time.Since(loadStart))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	started := time.Now()
	total, err := repo.publishPublicRecentDedupSnapshot(context.Background(), "measured-test-revision")
	elapsedBuild := time.Since(started)
	runtime.ReadMemStats(&after)
	if err != nil || total != int64(count) {
		t.Fatalf("snapshot build: count=%d want=%d err=%v", total, count, err)
	}
	t.Logf("snapshot rebuild %d entries: %s, Go total allocated during build %.1f MiB", count, elapsedBuild, float64(after.TotalAlloc-before.TotalAlloc)/1048576)

	durations := make([]time.Duration, 0, 20)
	page := count/10 - 1
	for i := 0; i < 20; i++ {
		started = time.Now()
		got, total, err := repo.readPublicRecentDedupSnapshotPage(context.Background(), "measured-test-revision", page, 10)
		if err != nil || total != int64(count) || len(got) != 10 {
			t.Fatalf("page read: total=%d len=%d err=%v", total, len(got), err)
		}
		durations = append(durations, time.Since(started))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	t.Logf("deep page= %d; p50=%s p95=%s (20 serial reads)", page, durations[10], durations[18])
	var head feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&head).Error; err != nil {
		t.Fatal(err)
	}
	var plan []struct {
		QueryPlan string `gorm:"column:QUERY PLAN"`
	}
	err = db.Raw("EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM feed_dedup_snapshot_rows WHERE generation_id = ? AND position >= ? ORDER BY position LIMIT 10", head.GenerationID, (page-1)*10).Scan(&plan).Error
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range plan {
		t.Logf("EXPLAIN %s", row.QueryPlan)
	}
}
