package feed

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"atoman/internal/testdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Test-only experiment. COPY is not wired into the Feed refresh worker or any
// production route. Use only with scripts/test-feed-copy-isolated.sh, which
// creates a disposable, resource-limited PostgreSQL 16 container.
func TestFeedSnapshotCopyVsGormIsolated(t *testing.T) {
	rawCount := os.Getenv("FEED_COPY_BENCH_COUNT")
	if rawCount == "" {
		t.Skip("opt-in synthetic COPY benchmark only")
	}
	if os.Getenv("FEED_COPY_BENCH_ISOLATED") != "yes" || os.Getenv("FEED_SCALE_ISOLATED") != "yes" {
		t.Fatal("COPY benchmark requires both isolation guards")
	}
	count, err := strconv.Atoi(rawCount)
	if err != nil || (count != 10000 && count != 100000 && count != 360000) {
		t.Fatalf("unsupported synthetic COPY count %q", rawCount)
	}

	db := testdb.Open(t)
	testdb.Migrate(t, db, &feedDedupSnapshotGeneration{}, &feedDedupSnapshotRow{}, &feedDedupSnapshotMember{}, &feedDedupSnapshotHead{})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	rows, members := feedCopySyntheticRows(count)
	t.Logf("COPY_BENCH_INIT count=%d representative_rows=%d members=%d", count, len(rows), len(members))

	baselineGen := uuid.New()
	copyGen := uuid.New()
	order := []string{"gorm", "copy"}
	if os.Getenv("FEED_COPY_BENCH_ORDER") == "copy-first" {
		order = []string{"copy", "gorm"}
	}
	stats := make(map[string]copyBenchStats, 2)
	var lastGeneration uuid.UUID
	for _, mode := range order {
		generation := baselineGen
		if mode == "copy" {
			generation = copyGen
		}
		lastGeneration = generation
		for j := range rows {
			rows[j].GenerationID = generation
		}
		for j := range members {
			members[j].GenerationID = generation
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		walBefore := copyBenchWAL(t, db)
		began := time.Now()
		var durationRows, durationMembers time.Duration
		if mode == "gorm" {
			durationRows, durationMembers, err = copyBenchGormWrite(ctx, db, generation, rows, members)
		} else {
			durationRows, durationMembers, err = copyBenchPGXWrite(ctx, db, generation, rows, members)
		}
		total := time.Since(began)
		if err != nil {
			t.Fatalf("%s transaction: %v", mode, err)
		}
		runtime.ReadMemStats(&after)
		stats[mode] = copyBenchStats{elapsed: total, rowTime: durationRows, memberTime: durationMembers,
			allocMiB: float64(after.TotalAlloc-before.TotalAlloc) / 1048576, walBytes: copyBenchWALDiff(t, db, walBefore), rssHwmKiB: copyBenchRSSHWMKiB()}
		t.Logf("COPY_BENCH_RESULT mode=%s count=%d total=%s rows=%s members=%s go_alloc_mib=%.1f wal_bytes=%d cumulative_rss_hwm_kib=%d",
			mode, count, total, durationRows, durationMembers, stats[mode].allocMiB, stats[mode].walBytes, stats[mode].rssHwmKiB)
		var head feedDedupSnapshotHead
		if err := db.Where("scope = ?", publicRecentDedupScope).Take(&head).Error; err != nil {
			t.Fatal(err)
		}
		if head.GenerationID != generation || head.RawItemCount != int64(count) || head.Total != int64(len(rows)) {
			t.Fatalf("%s head not atomically published: generation=%v total=%d raw=%d", mode, head.GenerationID, head.Total, head.RawItemCount)
		}
	}
	copyBenchCompareRows(t, db, baselineGen, copyGen, len(rows), len(members))
	copyBenchRollback(t, db, rows, members, lastGeneration)
	t.Logf("COPY_BENCH_SUMMARY count=%d gorm=%s copy=%s ratio=%.2f gorm_wal=%d copy_wal=%d",
		count, stats["gorm"].elapsed, stats["copy"].elapsed, float64(stats["gorm"].elapsed)/float64(stats["copy"].elapsed), stats["gorm"].walBytes, stats["copy"].walBytes)
}

type copyBenchStats struct {
	elapsed, rowTime, memberTime time.Duration
	allocMiB                     float64
	walBytes                     int64
	rssHwmKiB                    int64
}

func feedCopySyntheticRows(count int) ([]feedDedupSnapshotRow, []feedDedupSnapshotMember) {
	rows := make([]feedDedupSnapshotRow, 0, count-count/12)
	members := make([]feedDedupSnapshotMember, 0, count)
	epoch := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		itemID := uuid.NewSHA1(uuid.NameSpaceDNS, []byte(fmt.Sprintf("feed-copy-bench-item-%d", i)))
		pub := epoch.Add(-time.Duration(i) * 24 * time.Second)
		normalizedURL := fmt.Sprintf("https://synthetic.invalid/feeds/%d/articles/%d", i%961, i)
		title := fmt.Sprintf("Research digest article number %d on safety boundaries and large language models", i)
		members = append(members, feedDedupSnapshotMember{FeedItemID: itemID, PublishedAt: pub,
			NormalizedURL: normalizedURL, NormalizedURLHash: feedDedupURLHash(normalizedURL), NormalizedTitle: title})
		if i > 0 && i%12 == 0 {
			continue
		}
		duplicateIDs := json.RawMessage("[]")
		duplicateSources := json.RawMessage("[]")
		duplicates := 0
		if (i+1)%12 == 0 && i+1 < count {
			nested := uuid.NewSHA1(uuid.NameSpaceDNS, []byte(fmt.Sprintf("feed-copy-bench-item-%d", i+1)))
			duplicateIDs, _ = json.Marshal([]uuid.UUID{nested})
			duplicateSources, _ = json.Marshal([]string{fmt.Sprintf("Source %d", (i+1)%961)})
			duplicates = 1
		}
		rows = append(rows, feedDedupSnapshotRow{Position: int64(len(rows)), FeedItemID: itemID, PublishedAt: pub,
			NormalizedURL: normalizedURL, NormalizedTitle: title, DuplicateCount: duplicates,
			DuplicateMemberIDs: duplicateIDs, DuplicateSources: duplicateSources})
	}
	return rows, members
}

func copyBenchGormWrite(ctx context.Context, db *gorm.DB, generation uuid.UUID, rows []feedDedupSnapshotRow, members []feedDedupSnapshotMember) (time.Duration, time.Duration, error) {
	var rowTime, memberTime time.Duration
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&feedDedupSnapshotGeneration{GenerationID: generation, Scope: publicRecentDedupScope, Total: int64(len(rows)), BuiltAt: time.Now().UTC()}).Error; err != nil {
			return err
		}
		started := time.Now()
		for i := 0; i < len(rows); i += 350 {
			end := i + 350
			if end > len(rows) {
				end = len(rows)
			}
			if err := tx.Create(rows[i:end]).Error; err != nil {
				return err
			}
		}
		rowTime = time.Since(started)
		started = time.Now()
		for i := 0; i < len(members); i += 300 {
			end := i + 300
			if end > len(members) {
				end = len(members)
			}
			if err := tx.Create(members[i:end]).Error; err != nil {
				return err
			}
		}
		memberTime = time.Since(started)
		h := copyBenchHead(generation, len(rows), len(members))
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scope"}}, DoUpdates: clause.AssignmentColumns([]string{"generation_id", "revision", "total", "built_at", "source_versions", "tail_published_at", "tail_feed_item_id", "first_published_at", "first_feed_item_id", "position_base", "raw_item_count"})}).Create(&h).Error
	})
	return rowTime, memberTime, err
}

func copyBenchHead(generation uuid.UUID, rowCount, memberCount int) feedDedupSnapshotHead {
	return feedDedupSnapshotHead{Scope: publicRecentDedupScope, GenerationID: generation, Revision: "bench-only",
		Total: int64(rowCount), BuiltAt: time.Now().UTC(), SourceVersions: json.RawMessage("{}"), RawItemCount: int64(memberCount)}
}

// SQL and COPY must run on the same pgx connection and in the same tx;
// pgx.Conn is reached ONLY through the isolated GORM pool's sql.Conn.Raw.
type copyBenchHooks struct {
	afterCopy func() error
	afterHead func() error
}

func copyBenchPGXWrite(ctx context.Context, db *gorm.DB, generation uuid.UUID, rows []feedDedupSnapshotRow, members []feedDedupSnapshotMember) (time.Duration, time.Duration, error) {
	return copyBenchPGXWriteHooks(ctx, db, generation, rows, members, copyBenchHooks{})
}

func copyBenchPGXWriteHooks(ctx context.Context, db *gorm.DB, generation uuid.UUID, rows []feedDedupSnapshotRow, members []feedDedupSnapshotMember, hooks copyBenchHooks) (time.Duration, time.Duration, error) {
	sqlDB, err := db.DB()
	if err != nil {
		return 0, 0, err
	}
	connection, err := sqlDB.Conn(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer connection.Close()
	var rowTime, memberTime time.Duration
	err = connection.Raw(func(raw any) error {
		driverConn, ok := raw.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("COPY experiment requires pgx stdlib driver, got %T", raw)
		}
		tx, err := driverConn.Conn().Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		now := time.Now().UTC()
		if _, err := tx.Exec(ctx, "INSERT INTO feed_dedup_snapshot_generations (generation_id,scope,total,built_at) VALUES ($1,$2,$3,$4)", generation, publicRecentDedupScope, len(rows), now); err != nil {
			return err
		}
		started := time.Now()
		copied, err := tx.CopyFrom(ctx, pgx.Identifier{"feed_dedup_snapshot_rows"}, []string{
			"generation_id", "position", "feed_item_id", "published_at", "normalized_url", "normalized_title", "duplicate_count", "duplicate_member_ids", "duplicate_sources",
		}, pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			r := rows[i]
			return []any{r.GenerationID, r.Position, r.FeedItemID, r.PublishedAt, r.NormalizedURL, r.NormalizedTitle, r.DuplicateCount, string(r.DuplicateMemberIDs), string(r.DuplicateSources)}, nil
		}))
		rowTime = time.Since(started)
		if err != nil {
			return err
		}
		if copied != int64(len(rows)) {
			return fmt.Errorf("COPY representatives inserted %d, want %d", copied, len(rows))
		}
		started = time.Now()
		copied, err = tx.CopyFrom(ctx, pgx.Identifier{"feed_dedup_snapshot_members"}, []string{
			"generation_id", "feed_item_id", "published_at", "normalized_url", "normalized_url_hash", "normalized_title",
		}, pgx.CopyFromSlice(len(members), func(i int) ([]any, error) {
			m := members[i]
			return []any{m.GenerationID, m.FeedItemID, m.PublishedAt, m.NormalizedURL, m.NormalizedURLHash, m.NormalizedTitle}, nil
		}))
		memberTime = time.Since(started)
		if err != nil {
			return err
		}
		if copied != int64(len(members)) {
			return fmt.Errorf("COPY members inserted %d, want %d", copied, len(members))
		}
		if hooks.afterCopy != nil {
			if err := hooks.afterCopy(); err != nil {
				return err
			}
		}
		// Atomic head swap is part of the same pgx transaction as both COPY calls.
		_, err = tx.Exec(ctx, `INSERT INTO feed_dedup_snapshot_heads (scope,generation_id,revision,total,built_at,source_versions,tail_feed_item_id,first_feed_item_id,position_base,raw_item_count)
VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8,0,$9)
ON CONFLICT (scope) DO UPDATE SET generation_id=excluded.generation_id,revision=excluded.revision,total=excluded.total,built_at=excluded.built_at,source_versions=excluded.source_versions,tail_feed_item_id=excluded.tail_feed_item_id,first_feed_item_id=excluded.first_feed_item_id,position_base=excluded.position_base,raw_item_count=excluded.raw_item_count`,
			publicRecentDedupScope, generation, "bench-only", len(rows), time.Now().UTC(), "{}", uuid.Nil, uuid.Nil, len(members))
		if err != nil {
			return err
		}
		if hooks.afterHead != nil {
			if err := hooks.afterHead(); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	})
	return rowTime, memberTime, err
}

func copyBenchCompareRows(t *testing.T, db *gorm.DB, a, b uuid.UUID, rowCount, memberCount int) {
	t.Helper()
	var rowsDifferent int64
	err := db.Raw(`WITH a AS (SELECT * FROM feed_dedup_snapshot_rows WHERE generation_id = ?), b AS (SELECT * FROM feed_dedup_snapshot_rows WHERE generation_id = ?)
SELECT count(*) FROM a FULL JOIN b ON a.position=b.position
WHERE a.position IS NULL OR b.position IS NULL OR a.feed_item_id IS DISTINCT FROM b.feed_item_id OR a.published_at IS DISTINCT FROM b.published_at
OR a.normalized_url IS DISTINCT FROM b.normalized_url OR a.normalized_title IS DISTINCT FROM b.normalized_title
OR a.duplicate_count IS DISTINCT FROM b.duplicate_count OR a.duplicate_member_ids IS DISTINCT FROM b.duplicate_member_ids OR a.duplicate_sources IS DISTINCT FROM b.duplicate_sources`, a, b).Scan(&rowsDifferent).Error
	if err != nil || rowsDifferent != 0 {
		t.Fatalf("row full-field mismatch=%d err=%v", rowsDifferent, err)
	}
	var membersDifferent int64
	err = db.Raw(`WITH a AS (SELECT * FROM feed_dedup_snapshot_members WHERE generation_id = ?), b AS (SELECT * FROM feed_dedup_snapshot_members WHERE generation_id = ?)
SELECT count(*) FROM a FULL JOIN b ON a.feed_item_id=b.feed_item_id
WHERE a.feed_item_id IS NULL OR b.feed_item_id IS NULL OR a.published_at IS DISTINCT FROM b.published_at
OR a.normalized_url IS DISTINCT FROM b.normalized_url OR a.normalized_url_hash IS DISTINCT FROM b.normalized_url_hash OR a.normalized_title IS DISTINCT FROM b.normalized_title`, a, b).Scan(&membersDifferent).Error
	if err != nil || membersDifferent != 0 {
		t.Fatalf("member full-field mismatch=%d err=%v", membersDifferent, err)
	}
	var counts []int64
	if err := db.Raw("SELECT count(*) FROM feed_dedup_snapshot_rows WHERE generation_id IN (?,?) GROUP BY generation_id ORDER BY generation_id", a, b).Scan(&counts).Error; err != nil {
		t.Fatal(err)
	}
	if len(counts) != 2 || counts[0] != int64(rowCount) || counts[1] != int64(rowCount) {
		t.Fatalf("row count comparison failed: %v", counts)
	}
	counts = nil
	if err := db.Raw("SELECT count(*) FROM feed_dedup_snapshot_members WHERE generation_id IN (?,?) GROUP BY generation_id ORDER BY generation_id", a, b).Scan(&counts).Error; err != nil {
		t.Fatal(err)
	}
	if len(counts) != 2 || counts[0] != int64(memberCount) || counts[1] != int64(memberCount) {
		t.Fatalf("member count comparison failed: %v", counts)
	}
	t.Logf("COPY_BENCH_EQUAL rows=%d members=%d all_fields_match=true", rowCount, memberCount)
}

func copyBenchRollback(t *testing.T, db *gorm.DB, rows []feedDedupSnapshotRow, members []feedDedupSnapshotMember, current uuid.UUID) {
	t.Helper()
	badGen := uuid.New()
	// Force a UNIQUE constraint failure after the first COPY succeeds.
	failRow := []feedDedupSnapshotRow{rows[0]}
	failRow[0].GenerationID = badGen
	badMember := members[0]
	badMember.GenerationID = badGen
	failMembers := []feedDedupSnapshotMember{badMember, badMember}
	_, _, err := copyBenchPGXWrite(context.Background(), db, badGen, failRow, failMembers)
	if err == nil {
		t.Fatal("injected duplicate member unexpectedly committed")
	}
	var n int64
	if err := db.Model(&feedDedupSnapshotGeneration{}).Where("generation_id = ?", badGen).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("generation leaked from aborted COPY: %d %v", n, err)
	}
	if err := db.Model(&feedDedupSnapshotRow{}).Where("generation_id = ?", badGen).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("rows leaked from aborted COPY: %d %v", n, err)
	}
	if err := db.Model(&feedDedupSnapshotMember{}).Where("generation_id = ?", badGen).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("members leaked from aborted COPY: %d %v", n, err)
	}
	var head feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&head).Error; err != nil || head.GenerationID != current {
		t.Fatalf("head changed after rollback: %v %v", head.GenerationID, err)
	}
	t.Logf("COPY_BENCH_ROLLBACK atomic=true injected_unique_constraint_violation=true")
}

func copyBenchWAL(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var wal string
	if err := db.Raw("SELECT pg_current_wal_insert_lsn()::text").Scan(&wal).Error; err != nil {
		t.Fatal(err)
	}
	return wal
}
func copyBenchWALDiff(t *testing.T, db *gorm.DB, before string) int64 {
	t.Helper()
	var bytes float64
	if err := db.Raw("SELECT pg_wal_lsn_diff(pg_current_wal_insert_lsn(), ?::pg_lsn)", before).Scan(&bytes).Error; err != nil {
		t.Fatal(err)
	}
	return int64(bytes)
}

// Force the compile-time driver symbol to stay explicit: the experiment
// does not rely on reflection or driver-internal unsafe access.
var _ driver.Conn = (*stdlib.Conn)(nil)

// Two concurrent COPY writers must serialize their scope-head UPDATE while
// ordinary readers continue to see the previous complete generation. This
// exercises the same PostgreSQL MVCC/row-lock behavior as a refresh race.
func TestFeedSnapshotCopyReaderAtomicityIsolated(t *testing.T) {
	if os.Getenv("FEED_COPY_BENCH_COUNT") == "" {
		t.Skip("opt-in COPY benchmark only")
	}
	if os.Getenv("FEED_COPY_BENCH_ISOLATED") != "yes" || os.Getenv("FEED_SCALE_ISOLATED") != "yes" {
		t.Fatal("requires isolated COPY benchmark")
	}
	db := testdb.Open(t)
	testdb.Migrate(t, db, &feedDedupSnapshotGeneration{}, &feedDedupSnapshotRow{}, &feedDedupSnapshotMember{}, &feedDedupSnapshotHead{})
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// The two writers hold separate connections; a third must remain available
	// for a SELECT while the head row is exclusively locked.
	sqlDB.SetMaxOpenConns(6)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, members := feedCopySyntheticRows(240)
	baseline := uuid.New()
	for i := range rows {
		rows[i].GenerationID = baseline
	}
	for i := range members {
		members[i].GenerationID = baseline
	}
	if _, _, err := copyBenchGormWrite(ctx, db, baseline, rows, members); err != nil {
		t.Fatal(err)
	}
	newA, newB := uuid.New(), uuid.New()
	for i := range rows {
		rows[i].GenerationID = newA
	}
	for i := range members {
		members[i].GenerationID = newA
	}
	afterHeadA := make(chan struct{})
	releaseA := make(chan struct{})
	defer func() {
		select {
		case <-releaseA:
		default:
			close(releaseA)
		}
	}()
	doneA := make(chan error, 1)
	go func() {
		_, _, err := copyBenchPGXWriteHooks(ctx, db, newA, rows, members, copyBenchHooks{
			afterHead: func() error {
				close(afterHeadA)
				select {
				case <-releaseA:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		})
		doneA <- err
	}()
	select {
	case <-afterHeadA:
	case <-ctx.Done():
		t.Fatalf("first COPY writer did not acquire scope lock: %v", ctx.Err())
	}
	// Writer A has inserted all data and updated the scope head but has not
	// committed. Other sessions must not see any of these uncommitted changes.
	assertOld := func() {
		var head feedDedupSnapshotHead
		if err := db.WithContext(ctx).Where("scope = ?", publicRecentDedupScope).Take(&head).Error; err != nil {
			t.Fatal(err)
		}
		if head.GenerationID != baseline {
			t.Fatalf("reader saw uncommitted head %s, want %s", head.GenerationID, baseline)
		}
		var visible int64
		if err := db.WithContext(ctx).Model(&feedDedupSnapshotRow{}).Where("generation_id = ?", newA).Count(&visible).Error; err != nil {
			t.Fatal(err)
		}
		if visible != 0 {
			t.Fatalf("reader saw %d uncommitted rows", visible)
		}
	}
	assertOld()
	rowsB := append([]feedDedupSnapshotRow(nil), rows...)
	membersB := append([]feedDedupSnapshotMember(nil), members...)
	for i := range rowsB {
		rowsB[i].GenerationID = newB
	}
	for i := range membersB {
		membersB[i].GenerationID = newB
	}
	beforeHeadB := make(chan struct{})
	doneB := make(chan error, 1)
	go func() {
		_, _, err := copyBenchPGXWriteHooks(ctx, db, newB, rowsB, membersB, copyBenchHooks{
			afterCopy: func() error { close(beforeHeadB); return nil },
		})
		doneB <- err
	}()
	select {
	case <-beforeHeadB:
	case <-ctx.Done():
		t.Fatalf("second COPY writer did not finish copying: %v", ctx.Err())
	}
	select {
	case err := <-doneB:
		t.Fatalf("second writer bypassed uncommitted head lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	assertOld()
	close(releaseA)
	select {
	case err := <-doneA:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatalf("writer A commit: %v", ctx.Err())
	}
	select {
	case err := <-doneB:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatalf("writer B serialize: %v", ctx.Err())
	}
	var head feedDedupSnapshotHead
	if err := db.WithContext(ctx).Where("scope = ?", publicRecentDedupScope).Take(&head).Error; err != nil {
		t.Fatal(err)
	}
	if head.GenerationID != newB {
		t.Fatalf("last committed writer did not publish complete scope: %s want %s", head.GenerationID, newB)
	}
	for _, generation := range []uuid.UUID{baseline, newA, newB} {
		var n int64
		if err := db.WithContext(ctx).Model(&feedDedupSnapshotRow{}).Where("generation_id = ?", generation).Count(&n).Error; err != nil || n != int64(len(rows)) {
			t.Fatalf("generation %s rows=%d err=%v", generation, n, err)
		}
		if err := db.WithContext(ctx).Model(&feedDedupSnapshotMember{}).Where("generation_id = ?", generation).Count(&n).Error; err != nil || n != int64(len(members)) {
			t.Fatalf("generation %s members=%d err=%v", generation, n, err)
		}
	}
	t.Logf("COPY_BENCH_MVCC old_head_visible_during_lock=true writer_serialization=true all_generations_complete=true")
}

// Linux VmHWM is a cumulative per-process high-water mark; for paired runs
// only the FIRST method's value approximates a standalone peak. The second
// method's value must never be presented as its independent RSS peak.
func copyBenchRSSHWMKiB() int64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		words := strings.Fields(line)
		if len(words) < 2 {
			return 0
		}
		value, _ := strconv.ParseInt(words[1], 10, 64)
		return value
	}
	return 0
}
