package feed

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
)

// Run explicitly in scripts/test-isolated.sh using FEED_TAIL_INCREMENTAL_BENCHMARK=10000.
// The benchmark measures a fully verified old-to-new incremental transition,
// not a production RSS workload or a general new-item insertion.
func TestIncrementalTailMeasuredBenchmark(t *testing.T) {
	raw := os.Getenv("FEED_TAIL_INCREMENTAL_BENCHMARK")
	if raw == "" {
		t.Skip("requires FEED_TAIL_INCREMENTAL_BENCHMARK")
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 1 || count > 100000 {
		t.Fatalf("invalid fixture size %q", raw)
	}
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	fixtures := make([]model.FeedItem, count)
	for i := range fixtures {
		fixtures[i] = model.FeedItem{
			FeedSourceID: source.ID, GUID: fmt.Sprintf("tail-bench-%06d", i), Title: uuid.NewString(),
			Link:        fmt.Sprintf("https://benchmark.example.org/article/%d", i),
			PublishedAt: base.Add(-time.Duration(i) * time.Second),
		}
	}
	t0 := time.Now()
	if err := db.CreateInBatches(fixtures, 250).Error; err != nil {
		t.Fatal(err)
	}
	t.Logf("inserted %d isolated articles: %s", count, time.Since(t0))
	fixtures = nil
	t0 = time.Now()
	total, err := repo.publishPublicRecentDedupSnapshotTracked(context.Background())
	if err != nil || total != int64(count) {
		t.Fatalf("full snapshot total=%d err=%v", total, err)
	}
	t.Logf("full tracked rebuild: %s", time.Since(t0))

	// The first appended item is close to all others in time (worst case for
	// representative candidate filtering) but distinct in URL/title.
	tail := model.FeedItem{FeedSourceID: source.ID, GUID: "tail-bench-near", Title: uuid.NewString(), Link: "https://benchmark.example.net/independent-near-tail", PublishedAt: base.Add(-time.Duration(count) * time.Second)}
	if err := db.Create(&tail).Error; err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	t0 = time.Now()
	state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	elapsed := time.Since(t0)
	runtime.ReadMemStats(&after)
	if err != nil || state != PublicDedupRefreshIncremental {
		t.Fatalf("expected incremental: state=%q err=%v", state, err)
	}
	t.Logf("append near-tail with %d candidates: %s; total Go allocation %.1f MiB", count, elapsed, float64(after.TotalAlloc-before.TotalAlloc)/1048576)

	// A far-older item has no title candidates inside the 72-hour window.
	distant := model.FeedItem{FeedSourceID: source.ID, GUID: "tail-bench-far", Title: uuid.NewString(), Link: "https://benchmark.example.net/independent-old-tail", PublishedAt: tail.PublishedAt.Add(-100 * time.Hour)}
	if err := db.Create(&distant).Error; err != nil {
		t.Fatal(err)
	}
	t0 = time.Now()
	state, err = repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshIncremental {
		t.Fatalf("expected far-tail incremental: state=%q err=%v", state, err)
	}
	t.Logf("append distant tail: %s", time.Since(t0))
	final, total, err := repo.readPublicRecentDedupSnapshotPageTracked(context.Background(), count/10+1, 10)
	if err != nil || total != int64(count+2) || len(final) != 2 {
		t.Fatalf("deep page/count mismatch: total=%d pageLen=%d err=%v", total, len(final), err)
	}
}
