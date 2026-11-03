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

// Only via scripts/test-isolated.sh and explicit FEED_PREFIX_BENCHMARK=10000.
// It measures one latest RSS article against a realistic bounded date window
// of historical metadata, not a real production deployment or SLA.
func TestPrefixMeasuredBenchmark(t *testing.T) {
	raw := os.Getenv("FEED_PREFIX_BENCHMARK")
	if raw == "" {
		t.Skip("requires FEED_PREFIX_BENCHMARK")
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 1 || count > 100000 {
		t.Fatalf("invalid benchmark size %q", raw)
	}
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	step := time.Second
	if count >= 100000 {
		step = 12 * time.Second
	}
	articles := make([]model.FeedItem, count)
	for i := range articles {
		articles[i] = model.FeedItem{FeedSourceID: source.ID, GUID: fmt.Sprintf("prefix-benchmark-%06d", i), Title: uuid.NewString(), Link: fmt.Sprintf("https://benchmark.example.net/feed-item/%d", i), PublishedAt: base.Add(-time.Duration(i) * step)}
	}
	started := time.Now()
	if err := db.CreateInBatches(articles, 250).Error; err != nil {
		t.Fatal(err)
	}
	t.Logf("seed %d articles: %s", count, time.Since(started))
	articles = nil
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started = time.Now()
	initial, err := repo.publishPublicRecentDedupSnapshotTracked(context.Background())
	if err != nil || initial != int64(count) {
		t.Fatalf("full projection: total=%d err=%v", initial, err)
	}
	runtime.ReadMemStats(&after)
	t.Logf("full build incl raw-member indexes: %s; Go allocated %.1f MiB", time.Since(started), float64(after.TotalAlloc-before.TotalAlloc)/1048576)
	newest := model.FeedItem{FeedSourceID: source.ID, GUID: "prefix-benchmark-latest", Title: uuid.NewString(), Link: "https://another-benchmark.example.org/recent", PublishedAt: base.Add(time.Minute)}
	if err := db.Create(&newest).Error; err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&before)
	started = time.Now()
	state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	duration := time.Since(started)
	runtime.ReadMemStats(&after)
	if err != nil || state != PublicDedupRefreshIncremental {
		t.Fatalf("expected new latest incremental, state=%q err=%v", state, err)
	}
	t.Logf("latest single-item prefix insert: %s; Go allocated %.1f MiB", duration, float64(after.TotalAlloc-before.TotalAlloc)/1048576)
	first, total, err := repo.readPublicRecentDedupSnapshotPageTracked(context.Background(), 1, 10)
	if err != nil || total != int64(count+1) || len(first) != 10 || first[0].ID != newest.ID {
		t.Fatalf("newest page: total=%d len=%d err=%v", total, len(first), err)
	}
	pageNumber := count / 10
	deep, total, err := repo.readPublicRecentDedupSnapshotPageTracked(context.Background(), pageNumber, 10)
	if err != nil || total != int64(count+1) || len(deep) != 10 {
		t.Fatalf("deep page: total=%d len=%d err=%v", total, len(deep), err)
	}
	t.Logf("verified 1st page and page %d/total %d", pageNumber, total)
}
