package feed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestDedupIsolatedProductionScale is intentionally opt-in. Execute ONLY via
// scripts/test-feed-scale-isolated.sh, which creates a disposable, bounded
// PostgreSQL container. Synthetic data deliberately excludes private content.
// This does not constitute a production-data copy or an end-to-end API SLA.
func TestDedupIsolatedProductionScale(t *testing.T) {
	raw := os.Getenv("FEED_SCALE_COUNT")
	if raw == "" {
		t.Skip("explicit FEED_SCALE_COUNT and isolated test runner required")
	}
	if os.Getenv("FEED_SCALE_ISOLATED") != "yes" {
		t.Fatal("refusing scale test without the isolated-runner guard")
	}
	count, err := strconv.Atoi(raw)
	if err != nil || (count != 10000 && count != 100000 && count != 360000) {
		t.Fatalf("unsupported isolated scale count: %q", raw)
	}
	repo, db, firstSource := prepareDedupSnapshotTest(t)
	db.Logger = logger.Default.LogMode(logger.Silent)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(12)
	sqlDB.SetMaxIdleConns(4)
	sourceCount := 80
	if count == 10000 {
		sourceCount = 40
	}
	t.Logf("SCALE_PROFILE count=%d sources=%d duplicate_every=12 interval=24s fulltext_every=20", count, sourceCount)
	sourceIDs := []string{firstSource.ID.String()}
	for i := 1; i < sourceCount; i++ {
		source := model.FeedSource{SourceType: "external_rss", Title: fmt.Sprintf("Synthetic publisher %d", i),
			Hash: fmt.Sprintf("scale-20261103-%d", i), RssURL: fmt.Sprintf("https://scale.invalid/feed/%d", i)}
		if err := db.Create(&source).Error; err != nil {
			t.Fatal(err)
		}
		sourceIDs = append(sourceIDs, source.ID.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	startSeed := time.Now()
	// Chunked INSERT...SELECT avoids keeping 360k Go model objects in memory.
	// Source and URL overlap emulate multi-publisher syndicated articles. All
	// IDs, titles, links, dates, and bodies are deterministic synthetic data.
	const seedSQL = `INSERT INTO feed_items
        (id, feed_source_id, guid, title, link, published_at, created_at, updated_at, full_text_html)
        SELECT gen_random_uuid(), publisher.ids[(g % ?)+1],
               'scale-guid-' || g::text,
               md5((CASE WHEN g > 0 AND g % 12 = 0 THEN g-1 ELSE g END)::text)
               || md5(((CASE WHEN g > 0 AND g % 12 = 0 THEN g-1 ELSE g END) * 17)::text),
               'https://scale.invalid/story/' || (CASE WHEN g > 0 AND g % 12 = 0 THEN g-1 ELSE g END)::text,
               TIMESTAMPTZ '2026-11-03 00:00:00+00' - (g * interval '24 seconds'),
               now(), now(),
               CASE WHEN g % 20 = 0 THEN repeat('Independent article body. ', 48) ELSE '' END
        FROM generate_series(CAST(? AS bigint), CAST(? AS bigint)) AS seq(g)
        CROSS JOIN (SELECT CAST(? AS uuid[]) AS ids) AS publisher`
	const seedBatch = 5000
	for start := 0; start < count; start += seedBatch {
		end := start + seedBatch - 1
		if end >= count {
			end = count - 1
		}
		if err := db.WithContext(ctx).Exec(seedSQL, sourceCount, start, end, pq.Array(sourceIDs)).Error; err != nil {
			t.Fatalf("seed batch %d-%d: %v", start, end, err)
		}
		// A recurring database size guard stops the synthetic fixture before
		// it consumes unexpected disk space. The disposable container is also
		// removed by the runner regardless of the test's outcome.
		if end == count-1 || end%25000 == 24999 {
			if size := scaleDatabaseBytes(t, db); size > 1900*1024*1024 {
				t.Fatalf("synthetic test database exceeded 1.9GiB guard: %d", size)
			}
		}
	}
	t.Logf("SCALE_SEED count=%d elapsed=%s db_bytes=%d", count, time.Since(startSeed), scaleDatabaseBytes(t, db))
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var memBefore, memAfter runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	startBuild := time.Now()
	total, err := repo.publishPublicRecentDedupSnapshotTracked(ctx)
	runtime.ReadMemStats(&memAfter)
	expected := int64(count - (count-1)/12)
	if err != nil || total != expected {
		t.Fatalf("full projection expected=%d got=%d err=%v", expected, total, err)
	}
	t.Logf("SCALE_BUILD count=%d groups=%d elapsed=%s alloc_mib=%.1f heap_mib=%.1f heap_sys_mib=%.1f rss_hwm_kib=%d",
		count, total, time.Since(startBuild), float64(memAfter.TotalAlloc-memBefore.TotalAlloc)/(1024*1024),
		float64(memAfter.HeapAlloc)/(1024*1024), float64(memAfter.HeapSys)/(1024*1024), scaleRssPeakKiB())
	t.Logf("SCALE_STORAGE feed_items_bytes=%d snapshot_rows_bytes=%d snapshot_members_bytes=%d db_bytes=%d",
		scaleRelationBytes(t, db, "feed_items"), scaleRelationBytes(t, db, "feed_dedup_snapshot_rows"),
		scaleRelationBytes(t, db, "feed_dedup_snapshot_members"), scaleDatabaseBytes(t, db))

	// The snapshot uses an index seek into deep pages; capture 25 serial reads
	// rather than presenting isolated one-off timing as a production P95.
	deepPage := int(expected/10) - 1
	if deepPage < 1 {
		deepPage = 1
	}
	for _, selected := range []int{1, deepPage} {
		durations := make([]time.Duration, 0, 25)
		for iteration := 0; iteration < 25; iteration++ {
			started := time.Now()
			items, gotTotal, err := repo.readPublicRecentDedupSnapshotPageTracked(ctx, selected, 10)
			durations = append(durations, time.Since(started))
			if err != nil || gotTotal != expected || len(items) != 10 {
				t.Fatalf("page %d iteration %d: total=%d count=%d error=%v", selected, iteration, gotTotal, len(items), err)
			}
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		t.Logf("SCALE_PAGE page=%d page_size=10 reads=25 p50=%s p95=%s max=%s", selected, durations[12], durations[23], durations[24])
	}
	var head feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&head).Error; err != nil {
		t.Fatal(err)
	}
	var plans []struct {
		Plan string `gorm:"column:QUERY PLAN"`
	}
	startPosition := head.PositionBase + int64((deepPage-1)*10)
	if err := db.Raw(`EXPLAIN (ANALYZE, BUFFERS) SELECT position FROM feed_dedup_snapshot_rows
        WHERE generation_id = ? AND position >= ? ORDER BY position ASC LIMIT 10`,
		head.GenerationID, startPosition).Scan(&plans).Error; err != nil {
		t.Fatal(err)
	}
	for _, plan := range plans {
		t.Logf("SCALE_EXPLAIN %s", plan.Plan)
	}
	if len(plans) == 0 {
		t.Fatal("EXPLAIN returned no plan")
	}

	// One proven-safe latest insert should reuse the active generation, even
	// when source revisions are tracked. It must NOT scan 360k full bodies.
	newest := model.FeedItem{FeedSourceID: firstSource.ID, GUID: "scale-benchmark-newest",
		Title: uuid.NewString() + uuid.NewString(), Link: "https://scale.invalid/newest/unique",
		PublishedAt: time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)}
	if err := db.Create(&newest).Error; err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	state, err := repo.refreshPublicRecentDedupIfQuiet(ctx, PublicDedupRefreshPolicy{MinFullRebuildInterval: time.Hour})
	if err != nil || state != PublicDedupRefreshIncremental {
		t.Fatalf("safe 360k-prefix insert state=%s err=%v", state, err)
	}
	t.Logf("SCALE_PREFIX count=%d elapsed=%s", count, time.Since(t0))
	firstPage, gotTotal, err := repo.readPublicRecentDedupSnapshotPageTracked(ctx, 1, 10)
	if err != nil || gotTotal != expected+1 || len(firstPage) != 10 || firstPage[0].ID != newest.ID {
		t.Fatalf("incremental head incorrect: total=%d first=%v err=%v", gotTotal, firstPage, err)
	}

	// Eight source-specific writers, four INSERT statements each; concurrent
	// ingestion should not wait on one global revision lock. Since sources
	// differ, multi-source change cannot be treated as a safe one-source
	// prefix. Cooldown must defer an expensive rebuild, and reads stay stale.
	const writers, bursts = 8, 4
	type result struct {
		duration time.Duration
		err      error
	}
	results := make(chan result, writers*bursts)
	var group sync.WaitGroup
	begin := make(chan struct{})
	for writer := 0; writer < writers; writer++ {
		group.Add(1)
		go func(writer int) {
			defer group.Done()
			<-begin
			srcID := uuid.MustParse(sourceIDs[writer+1])
			for batch := 0; batch < bursts; batch++ {
				stories := make([]model.FeedItem, 4)
				for j := range stories {
					key := fmt.Sprintf("scale-burst-%d-%d-%d", writer, batch, j)
					stories[j] = model.FeedItem{FeedSourceID: srcID, GUID: key, Title: key + uuid.NewString(),
						Link:        "https://scale.invalid/burst/" + key,
						PublishedAt: time.Date(2026, 11, 3, 3, 0, 0, 0, time.UTC).Add(time.Duration(writer*100+batch*4+j) * time.Second)}
				}
				started := time.Now()
				writeCtx, writeCancel := context.WithTimeout(ctx, 8*time.Second)
				writeErr := db.WithContext(writeCtx).CreateInBatches(&stories, 4).Error
				writeCancel()
				results <- result{duration: time.Since(started), err: writeErr}
				if writeErr != nil {
					return
				}
			}
		}(writer)
	}
	close(begin)
	group.Wait()
	close(results)
	writeDurations := make([]time.Duration, 0, writers*bursts)
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent source burst failed: %v", result.err)
		}
		writeDurations = append(writeDurations, result.duration)
	}
	if len(writeDurations) != writers*bursts {
		t.Fatalf("concurrent burst missing statements: %d", len(writeDurations))
	}
	sort.Slice(writeDurations, func(i, j int) bool { return writeDurations[i] < writeDurations[j] })
	t.Logf("SCALE_WRITES sources=%d statements=%d items=%d p50=%s p95=%s max=%s",
		writers, len(writeDurations), writers*bursts*4, writeDurations[len(writeDurations)/2],
		writeDurations[30], writeDurations[len(writeDurations)-1])
	if _, _, err := repo.readPublicRecentDedupSnapshotPageTracked(ctx, 1, 10); !errors.Is(err, ErrDedupSnapshotStale) {
		t.Fatalf("concurrent RSS changes failed to invalidate existing projection: %v", err)
	}
	state, err = repo.refreshPublicRecentDedupIfQuiet(ctx, PublicDedupRefreshPolicy{MinFullRebuildInterval: time.Hour})
	if err != nil || state != PublicDedupRefreshDeferred {
		t.Fatalf("multi-source changes should defer full rebuild under cooldown: %s %v", state, err)
	}
	var journalCount int64
	if err := db.Model(&feedDedupChangeEvent{}).Count(&journalCount).Error; err != nil {
		t.Fatal(err)
	}
	if journalCount != 1+writers*bursts {
		t.Fatalf("unexpected journal event count %d", journalCount)
	}
	removed, err := repo.pruneDedupChangeEventsBeforeCheckpoint(ctx, 24*time.Hour)
	if err != nil || removed != 0 {
		t.Fatalf("fresh journal events deleted: removed=%d err=%v", removed, err)
	}
	if removed, err := repo.pruneInactiveDedupSnapshotGenerations(ctx, time.Hour); err != nil || removed != 0 {
		t.Fatalf("active generation improperly removed: %d %v", removed, err)
	}
	t.Logf("SCALE_FINAL revision_events=%d stale_read_rejected=true rebuild_deferred=true db_bytes=%d rss_hwm_kib=%d",
		journalCount, scaleDatabaseBytes(t, db), scaleRssPeakKiB())
}

func scaleDatabaseBytes(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	if err := db.Raw("SELECT pg_database_size(current_database())").Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func scaleRelationBytes(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	if !strings.HasPrefix(table, "feed_") {
		t.Fatal("unsafe scale relation")
	}
	var count int64
	if err := db.Raw("SELECT pg_total_relation_size(CAST(? AS regclass))", table).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func scaleRssPeakKiB() int64 {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		amount, _ := strconv.ParseInt(fields[1], 10, 64)
		return amount
	}
	return 0
}
