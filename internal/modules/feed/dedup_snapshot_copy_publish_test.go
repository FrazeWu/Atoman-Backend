package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestSnapshotCopyExperimentTargetGuard(t *testing.T) {
	for _, c := range []struct {
		db, schema string
		allowed    bool
	}{
		{"atoman", "public", false},
		{"atoman", "test_fixture", false},
		{"atoman_test", "public", false},
		{"atoman_test", "test_abc", false},
		{"test", "test_abc", false},
		{"test_fixture", "test_0123456789abcdef0123456789abcdef", true},
		{"not_test_live", "test_0123456789abcdef0123456789abcdef", false},
		{"atoman_test", "test_0123456789abcdef0123456789abcdef", true},
		{"atoman", "test_0123456789abcdef0123456789abcdef", false},
	} {
		if got := snapshotCopyTargetAllowed(c.db, c.schema); got != c.allowed {
			t.Fatalf("target %s/%s allowed=%v, want=%v", c.db, c.schema, got, c.allowed)
		}
	}
}

func TestSnapshotCopyExperimentGuard(t *testing.T) {
	t.Setenv("FEED_SCALE_ISOLATED", "")
	t.Setenv("FEED_SNAPSHOT_COPY_EXPERIMENT", "yes")
	enabled, err := snapshotCopyExperimentEnabled(context.Background(), nil)
	if err != nil || enabled {
		t.Fatalf("isolated flag must also be present: %v %v", enabled, err)
	}
	t.Setenv("FEED_SCALE_ISOLATED", "yes")
	t.Setenv("FEED_SNAPSHOT_COPY_EXPERIMENT", "")
	enabled, err = snapshotCopyExperimentEnabled(context.Background(), nil)
	if err != nil || enabled {
		t.Fatalf("experiment flag must also be present: %v %v", enabled, err)
	}
}

func snapshotCopyFixture(t *testing.T, db *gorm.DB, source model.FeedSource) {
	t.Helper()
	ts := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	fixtures := []model.FeedItem{
		{FeedSourceID: source.ID, Title: "Unicode report 中文 🧪", Link: "https://example.invalid/a?utm_source=abc", GUID: "copy-parity-a", PublishedAt: ts},
		{FeedSourceID: source.ID, Title: "Unicode report 中文 🧪", Link: "https://example.invalid/a", GUID: "copy-parity-a2", PublishedAt: ts.Add(-time.Minute)},
		{FeedSourceID: source.ID, Title: "An unrelated report about daily weather", Link: "https://example.invalid/c", GUID: "copy-parity-c", PublishedAt: ts.Add(-2 * time.Minute)},
		{FeedSourceID: source.ID, Title: "Special punctuation in a JSON report 1", Link: "https://example.invalid/d", GUID: "copy-parity-d", PublishedAt: ts.Add(-3 * time.Minute)},
		{FeedSourceID: source.ID, Title: "Special punctuation in a JSON report 2", Link: "https://example.invalid/e", GUID: "copy-parity-e", PublishedAt: ts.Add(-4 * time.Minute)},
	}
	if err := db.Create(&fixtures).Error; err != nil {
		t.Fatal(err)
	}
}

func snapshotCopyCompareGenerations(t *testing.T, db *gorm.DB, a, b uuid.UUID) {
	t.Helper()
	var diff int64
	checks := []string{
		`WITH a AS (SELECT * FROM feed_dedup_snapshot_rows WHERE generation_id=?),b AS (SELECT * FROM feed_dedup_snapshot_rows WHERE generation_id=?)
SELECT count(*) FROM a FULL JOIN b ON a.position=b.position WHERE a.position IS NULL OR b.position IS NULL OR
 a.feed_item_id IS DISTINCT FROM b.feed_item_id OR a.published_at IS DISTINCT FROM b.published_at OR
 a.normalized_url IS DISTINCT FROM b.normalized_url OR a.normalized_title IS DISTINCT FROM b.normalized_title OR
 a.duplicate_count IS DISTINCT FROM b.duplicate_count OR a.duplicate_member_ids IS DISTINCT FROM b.duplicate_member_ids OR a.duplicate_sources IS DISTINCT FROM b.duplicate_sources`,
		`WITH a AS (SELECT * FROM feed_dedup_snapshot_members WHERE generation_id=?),b AS (SELECT * FROM feed_dedup_snapshot_members WHERE generation_id=?)
SELECT count(*) FROM a FULL JOIN b ON a.feed_item_id=b.feed_item_id WHERE a.feed_item_id IS NULL OR b.feed_item_id IS NULL OR
 a.published_at IS DISTINCT FROM b.published_at OR a.normalized_url IS DISTINCT FROM b.normalized_url OR
 a.normalized_url_hash IS DISTINCT FROM b.normalized_url_hash OR a.normalized_title IS DISTINCT FROM b.normalized_title`,
	}
	for i, sql := range checks {
		if err := db.Raw(sql, a, b).Scan(&diff).Error; err != nil {
			t.Fatal(err)
		}
		if diff != 0 {
			t.Fatalf("COPY/GORM difference in projection table %d: %d", i, diff)
		}
	}
}

func TestSnapshotCopyFullPublishParityAndFallback(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	snapshotCopyFixture(t, db, source)
	versions := map[string]int64{"feed-source-1": 17, "another": 2}
	count, err := repo.publishPublicRecentDedupSnapshotWithCheckpoints(context.Background(), "gorm-before-copy", versions)
	if err != nil {
		t.Fatal(err)
	}
	var gormHead feedDedupSnapshotHead
	if err := db.Where("scope=?", publicRecentDedupScope).Take(&gormHead).Error; err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEED_SCALE_ISOLATED", "yes")
	t.Setenv("FEED_SNAPSHOT_COPY_EXPERIMENT", "yes")
	copyCount, err := repo.publishPublicRecentDedupSnapshotWithCheckpoints(context.Background(), "copy-snapshot", versions)
	if err != nil {
		t.Fatal(err)
	}
	if count != copyCount {
		t.Fatalf("GORM groups %d, COPY groups %d", count, copyCount)
	}
	var copyHead feedDedupSnapshotHead
	if err := db.Where("scope=?", publicRecentDedupScope).Take(&copyHead).Error; err != nil {
		t.Fatal(err)
	}
	if copyHead.GenerationID == gormHead.GenerationID || copyHead.Revision != "copy-snapshot" || copyHead.Total != gormHead.Total || copyHead.RawItemCount != gormHead.RawItemCount ||
		copyHead.FirstFeedItemID != gormHead.FirstFeedItemID || copyHead.TailFeedItemID != gormHead.TailFeedItemID || copyHead.FirstPublishedAt == nil || copyHead.TailPublishedAt == nil ||
		!copyHead.FirstPublishedAt.Equal(*gormHead.FirstPublishedAt) || !copyHead.TailPublishedAt.Equal(*gormHead.TailPublishedAt) ||
		string(copyHead.SourceVersions) != string(gormHead.SourceVersions) {
		t.Fatalf("head parity failed before=%+v after=%+v", gormHead, copyHead)
	}
	var checkpoint map[string]int64
	if err := json.Unmarshal(copyHead.SourceVersions, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if len(checkpoint) != 2 || checkpoint["feed-source-1"] != 17 {
		t.Fatalf("checkpoints lost: %v", checkpoint)
	}
	snapshotCopyCompareGenerations(t, db, gormHead.GenerationID, copyHead.GenerationID)
	for _, page := range []int{1, 2, 3, 100} {
		got, total, err := repo.readPublicRecentDedupSnapshotPage(context.Background(), "copy-snapshot", page, 2)
		if err != nil {
			t.Fatal(err)
		}
		if total != copyCount || (page <= 2 && len(got) == 0) || (page == 100 && len(got) != 0) {
			t.Fatalf("bad page %d total=%d size=%d", page, total, len(got))
		}
	}
	if _, _, err := repo.readPublicRecentDedupSnapshotPage(context.Background(), "gorm-before-copy", 1, 2); err != ErrDedupSnapshotStale {
		t.Fatalf("old revision served: %v", err)
	}
	// Turning off the experiment must restore the unchanged GORM writer.
	t.Setenv("FEED_SNAPSHOT_COPY_EXPERIMENT", "")
	fallbackCount, err := repo.publishPublicRecentDedupSnapshotWithCheckpoints(context.Background(), "gorm-after-copy", versions)
	if err != nil || fallbackCount != copyCount {
		t.Fatalf("fallback failed: %d %v", fallbackCount, err)
	}
	var finalHead feedDedupSnapshotHead
	if err := db.Where("scope=?", publicRecentDedupScope).Take(&finalHead).Error; err != nil {
		t.Fatal(err)
	}
	snapshotCopyCompareGenerations(t, db, copyHead.GenerationID, finalHead.GenerationID)
	t.Logf("COPY_FULL_PARITY rows=%d members=%d snapshots=3 full_head_equal=true fields_equal=true pagination=true fallback_gorm=true", count, finalHead.RawItemCount)
}

func TestSnapshotCopyFullPublishFailuresRollBack(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	snapshotCopyFixture(t, db, source)
	if _, err := repo.publishPublicRecentDedupSnapshot(context.Background(), "safe-old-revision"); err != nil {
		t.Fatal(err)
	}
	var original feedDedupSnapshotHead
	if err := db.Where("scope=?", publicRecentDedupScope).Take(&original).Error; err != nil {
		t.Fatal(err)
	}
	countRows := func(table string) int64 {
		var n int64
		if err := db.Raw("SELECT count(*) FROM " + table).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	tables := []string{"feed_dedup_snapshot_generations", "feed_dedup_snapshot_rows", "feed_dedup_snapshot_members"}
	before := make([]int64, len(tables))
	for i, table := range tables {
		before[i] = countRows(table)
	}
	t.Setenv("FEED_SCALE_ISOLATED", "yes")
	t.Setenv("FEED_SNAPSHOT_COPY_EXPERIMENT", "yes")
	for _, fault := range []string{"before_generation", "before_rows", "during_rows", "after_rows", "during_members", "after_members", "before_head", "after_head"} {
		t.Setenv("FEED_SNAPSHOT_COPY_FAIL_AT", fault)
		revision := fmt.Sprintf("will-not-publish-%s", fault)
		_, err := repo.publishPublicRecentDedupSnapshot(context.Background(), revision)
		if err == nil || !strings.Contains(err.Error(), "injected COPY snapshot failure") {
			t.Fatalf("fault %s not propagated: %v", fault, err)
		}
		var got feedDedupSnapshotHead
		if err := db.Where("scope=?", publicRecentDedupScope).Take(&got).Error; err != nil {
			t.Fatal(err)
		}
		if got.GenerationID != original.GenerationID || got.Revision != original.Revision {
			t.Fatalf("head changed after fault %s", fault)
		}
		for i, table := range tables {
			if n := countRows(table); n != before[i] {
				t.Fatalf("fault %s left %d rows in %s versus %d", fault, n, table, before[i])
			}
		}
		if _, _, err := repo.readPublicRecentDedupSnapshotPage(context.Background(), "safe-old-revision", 1, 2); err != nil {
			t.Fatalf("fault %s invalidated old reader: %v", fault, err)
		}
	}
	t.Setenv("FEED_SNAPSHOT_COPY_FAIL_AT", "")
	if _, err := repo.publishPublicRecentDedupSnapshot(context.Background(), "recovered-copy-revision"); err != nil {
		t.Fatalf("COPY cannot recover after injected failures: %v", err)
	}
	t.Logf("COPY_FULL_ROLLBACK faults=8 old_head_stable=true no_orphans=true recovery=true")
}

func TestSnapshotCopyFullPublishEmptyProjection(t *testing.T) {
	repo, db, _ := prepareDedupSnapshotTest(t)
	t.Setenv("FEED_SCALE_ISOLATED", "yes")
	t.Setenv("FEED_SNAPSHOT_COPY_EXPERIMENT", "yes")
	n, err := repo.publishPublicRecentDedupSnapshot(context.Background(), "empty-copy")
	if err != nil || n != 0 {
		t.Fatalf("empty COPY projection %d %v", n, err)
	}
	var head feedDedupSnapshotHead
	if err := db.Where("scope=?", publicRecentDedupScope).Take(&head).Error; err != nil {
		t.Fatal(err)
	}
	if head.Total != 0 || head.RawItemCount != 0 || head.Revision != "empty-copy" {
		t.Fatalf("bad empty head: %+v", head)
	}
}
