package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"atoman/internal/testdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/gorm"
)

// TestDedupCopyIsolatedBenchmark is a standalone diagnostic, NOT a production
// write path. The testdb helper rejects production DSNs and owns a disposable
// schema. Its explicit environment gate prevents accidental 100k runs.
func TestDedupCopyIsolatedBenchmark(t *testing.T) {
	if os.Getenv("FEED_COPY_BENCHMARK") != "yes" {
		t.Skip("set FEED_COPY_BENCHMARK=yes in a dedicated isolated test DB")
	}
	n := 10000
	if v := os.Getenv("FEED_COPY_COUNT"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 || parsed > 100000 {
			t.Fatal("FEED_COPY_COUNT must be 1..100000")
		}
		n = parsed
	}
	db := testdb.Open(t)
	if err := db.AutoMigrate(&feedDedupSnapshotGeneration{}, &feedDedupSnapshotRow{}, &feedDedupSnapshotMember{}, &feedDedupSnapshotHead{}); err != nil {
		t.Fatal(err)
	}
	baselineGeneration, copyGeneration := uuid.New(), uuid.New()
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = uuid.New()
	}
	makeRow := func(id uuid.UUID, i int, gen uuid.UUID) feedDedupSnapshotRow {
		return feedDedupSnapshotRow{GenerationID: gen, Position: int64(i), FeedItemID: id, PublishedAt: base.Add(-time.Duration(i) * time.Second), NormalizedURL: fmt.Sprintf("https://example.invalid/post/%d", i), NormalizedTitle: fmt.Sprintf("Synthetic article %d with independent topic", i), DuplicateCount: 0, DuplicateMemberIDs: json.RawMessage("[]"), DuplicateSources: json.RawMessage("[]")}
	}
	makeMember := func(id uuid.UUID, i int, gen uuid.UUID) feedDedupSnapshotMember {
		return feedDedupSnapshotMember{GenerationID: gen, FeedItemID: id, PublishedAt: base.Add(-time.Duration(i) * time.Second), NormalizedURL: fmt.Sprintf("https://example.invalid/post/%d", i), NormalizedURLHash: fmt.Sprintf("%064x", i), NormalizedTitle: fmt.Sprintf("Synthetic article %d with independent topic", i)}
	}
	// Generate the same logical tuples for both approaches without retaining
	// two complete data sets in memory.
	baselineStart := time.Now()
	err := db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&feedDedupSnapshotGeneration{GenerationID: baselineGeneration, Scope: publicRecentDedupScope, Total: int64(n), BuiltAt: base}).Error; err != nil {
			return err
		}
		for i := 0; i < n; i += 350 {
			end := i + 350
			if end > n {
				end = n
			}
			rows := make([]feedDedupSnapshotRow, 0, end-i)
			for j := i; j < end; j++ {
				rows = append(rows, makeRow(ids[j], j, baselineGeneration))
			}
			if err := tx.Create(rows).Error; err != nil {
				return err
			}
		}
		for i := 0; i < n; i += 300 {
			end := i + 300
			if end > n {
				end = n
			}
			members := make([]feedDedupSnapshotMember, 0, end-i)
			for j := i; j < end; j++ {
				members = append(members, makeMember(ids[j], j, baselineGeneration))
			}
			if err := tx.Create(members).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("GORM baseline failed: %v", err)
	}
	baselineTime := time.Since(baselineStart)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	copyOnce := func(gen uuid.UUID, commit, duplicatePosition bool) (time.Duration, error) {
		ctx := context.Background()
		start := time.Now()
		conn, err := sqlDB.Conn(ctx)
		if err != nil {
			return 0, err
		}
		defer conn.Close()
		err = conn.Raw(func(raw any) error {
			driver, ok := raw.(*stdlib.Conn)
			if !ok {
				return fmt.Errorf("unexpected driver %T", raw)
			}
			pgconn := driver.Conn()
			tx, err := pgconn.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, "INSERT INTO feed_dedup_snapshot_generations (generation_id,scope,total,built_at) VALUES ($1,$2,$3,$4)", gen, publicRecentDedupScope, n, base); err != nil {
				return err
			}
			rowSource := pgx.CopyFromSlice(n, func(i int) ([]any, error) {
				r := makeRow(ids[i], i, gen)
				if duplicatePosition && i == n-1 {
					r.Position = 0
				}
				return []any{r.GenerationID, r.Position, r.FeedItemID, r.PublishedAt, r.NormalizedURL, r.NormalizedTitle, r.DuplicateCount, []byte(r.DuplicateMemberIDs), []byte(r.DuplicateSources)}, nil
			})
			count, err := tx.CopyFrom(ctx, pgx.Identifier{"feed_dedup_snapshot_rows"}, []string{"generation_id", "position", "feed_item_id", "published_at", "normalized_url", "normalized_title", "duplicate_count", "duplicate_member_ids", "duplicate_sources"}, rowSource)
			if err != nil {
				return err
			}
			if count != int64(n) {
				return fmt.Errorf("copy rows=%d want=%d", count, n)
			}
			memberSource := pgx.CopyFromSlice(n, func(i int) ([]any, error) {
				m := makeMember(ids[i], i, gen)
				return []any{m.GenerationID, m.FeedItemID, m.PublishedAt, m.NormalizedURL, m.NormalizedURLHash, m.NormalizedTitle}, nil
			})
			count, err = tx.CopyFrom(ctx, pgx.Identifier{"feed_dedup_snapshot_members"}, []string{"generation_id", "feed_item_id", "published_at", "normalized_url", "normalized_url_hash", "normalized_title"}, memberSource)
			if err != nil {
				return err
			}
			if count != int64(n) {
				return fmt.Errorf("copy members=%d want=%d", count, n)
			}
			if !commit {
				return errors.New("intentionally abort after both COPY operations")
			}
			return tx.Commit(ctx)
		})
		return time.Since(start), err
	}
	copyTime, err := copyOnce(copyGeneration, true, false)
	if err != nil {
		t.Fatalf("COPY commit failed: %v", err)
	}
	for _, gen := range []uuid.UUID{baselineGeneration, copyGeneration} {
		var rows, members, gens int64
		if err := db.Model(&feedDedupSnapshotRow{}).Where("generation_id = ?", gen).Count(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&feedDedupSnapshotMember{}).Where("generation_id = ?", gen).Count(&members).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&feedDedupSnapshotGeneration{}).Where("generation_id = ?", gen).Count(&gens).Error; err != nil {
			t.Fatal(err)
		}
		if rows != int64(n) || members != int64(n) || gens != 1 {
			t.Fatalf("generation %s rows=%d members=%d metadata=%d", gen, rows, members, gens)
		}
	}
	// Validate serialized fields and exact tuple equivalence, not just counts.
	var different int64
	if err := db.Raw(`SELECT count(*) FROM feed_dedup_snapshot_rows a JOIN feed_dedup_snapshot_rows b ON a.position=b.position AND b.generation_id=? WHERE a.generation_id=? AND (a.feed_item_id IS DISTINCT FROM b.feed_item_id OR a.published_at IS DISTINCT FROM b.published_at OR a.normalized_url IS DISTINCT FROM b.normalized_url OR a.normalized_title IS DISTINCT FROM b.normalized_title OR a.duplicate_count IS DISTINCT FROM b.duplicate_count OR a.duplicate_member_ids IS DISTINCT FROM b.duplicate_member_ids OR a.duplicate_sources IS DISTINCT FROM b.duplicate_sources)`, copyGeneration, baselineGeneration).Scan(&different).Error; err != nil {
		t.Fatal(err)
	}
	if different != 0 {
		t.Fatalf("COPY rows differ from GORM rows: %d", different)
	}
	if err := db.Raw(`SELECT count(*) FROM feed_dedup_snapshot_members a JOIN feed_dedup_snapshot_members b ON a.feed_item_id=b.feed_item_id AND b.generation_id=? WHERE a.generation_id=? AND (a.published_at IS DISTINCT FROM b.published_at OR a.normalized_url IS DISTINCT FROM b.normalized_url OR a.normalized_url_hash IS DISTINCT FROM b.normalized_url_hash OR a.normalized_title IS DISTINCT FROM b.normalized_title)`, copyGeneration, baselineGeneration).Scan(&different).Error; err != nil {
		t.Fatal(err)
	}
	if different != 0 {
		t.Fatalf("COPY members differ from GORM members: %d", different)
	}
	aborted := uuid.New()
	abortTime, abortErr := copyOnce(aborted, false, false)
	if abortErr == nil || abortErr.Error() != "intentionally abort after both COPY operations" {
		t.Fatalf("abort failure must be intentional, got: %v", abortErr)
	}
	var remaining int64
	for _, table := range []string{"feed_dedup_snapshot_generations", "feed_dedup_snapshot_rows", "feed_dedup_snapshot_members"} {
		if err := db.Raw("SELECT count(*) FROM "+table+" WHERE generation_id=?", aborted).Scan(&remaining).Error; err != nil {
			t.Fatal(err)
		}
		if remaining != 0 {
			t.Fatalf("rollback leaked %d records in %s", remaining, table)
		}
	}
	// A database constraint error in the middle of COPY must also abort the
	// entire transaction, including the generation metadata inserted first.
	conflictGeneration := uuid.New()
	conflictTime, conflictErr := copyOnce(conflictGeneration, true, true)
	if conflictErr == nil {
		t.Fatal("duplicate position unexpectedly committed")
	}
	for _, table := range []string{"feed_dedup_snapshot_generations", "feed_dedup_snapshot_rows", "feed_dedup_snapshot_members"} {
		if err := db.Raw("SELECT count(*) FROM "+table+" WHERE generation_id=?", conflictGeneration).Scan(&remaining).Error; err != nil {
			t.Fatal(err)
		}
		if remaining != 0 {
			t.Fatalf("constraint failure leaked %d records in %s", remaining, table)
		}
	}
	t.Logf("COPY_COMPARISON count=%d gorm_insert=%s pgx_copy=%s copy_abort=%s copy_constraint_error=%s same_data=true rollback=true", n, baselineTime, copyTime, abortTime, conflictTime)
}
