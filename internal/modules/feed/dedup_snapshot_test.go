package feed

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	legacyfeed "atoman/internal/service"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func prepareDedupSnapshotTest(t *testing.T) (*Repo, *gorm.DB, model.FeedSource) {
	t.Helper()
	service, db, _ := newFeedTestService(t)
	if err := db.AutoMigrate(&feedDedupSnapshotHead{}, &feedDedupSnapshotGeneration{}, &feedDedupSnapshotRow{}, &feedDedupSnapshotMember{}); err != nil {
		t.Fatalf("migrate isolated projection schema: %v", err)
	}
	if err := db.Exec("DELETE FROM feed_item_reads").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM feed_items").Error; err != nil {
		t.Fatal(err)
	}
	var source model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&source).Error; err != nil {
		t.Fatal(err)
	}
	return service.repo, db, source
}

func TestPublicRecentDedupSnapshotPagesMatchLegacy(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	mirrorSource := model.FeedSource{SourceType: "external_rss", Title: "Mirror", Hash: "dedup-snapshot-mirror", RssURL: "https://mirror.example/rss"}
	if err := db.Create(&mirrorSource).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	heavy := strings.Repeat("article full text data ", 500)
	fixtures := []model.FeedItem{
		{FeedSourceID: source.ID, Title: "Story A", Link: "https://example.org/story/a", GUID: "snapshot-a", PublishedAt: base.Add(5 * time.Minute), FullTextHTML: heavy},
		{FeedSourceID: mirrorSource.ID, Title: "Story A mirror", Link: "https://example.org/story/a?utm_campaign=x", GUID: "snapshot-a-mirror", PublishedAt: base.Add(4 * time.Minute)},
		{FeedSourceID: source.ID, Title: "A second unrelated news article", Link: "https://example.org/story/b", GUID: "snapshot-b", PublishedAt: base.Add(3 * time.Minute), ReaderHTML: "<p>full reader text</p>"},
		{FeedSourceID: mirrorSource.ID, Title: "A second unrelated news article!", Link: "https://example.org/mirror/b", GUID: "snapshot-b-mirror", PublishedAt: base.Add(2 * time.Minute)},
		{FeedSourceID: source.ID, Title: "The third independent headline", Link: "https://example.org/unique/c", GUID: "snapshot-c", PublishedAt: base.Add(time.Minute)},
	}
	for i := range fixtures {
		if err := db.Create(&fixtures[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	sourceRows, err := repo.ListExploreFeedItemsForDuplicateScan("recent", FeedQuery{})
	if err != nil {
		t.Fatal(err)
	}
	legacyfeed.AnnotateDuplicateFeedItems(sourceRows)
	expected := make([]model.FeedItem, 0, len(sourceRows))
	for _, item := range sourceRows {
		if !item.IsDuplicate {
			expected = append(expected, item)
		}
	}

	count, err := repo.publishPublicRecentDedupSnapshot(context.Background(), "snapshot-revision-001")
	if err != nil {
		t.Fatalf("publish snapshot: %v", err)
	}
	if count != int64(len(expected)) {
		t.Fatalf("count=%d want %d", count, len(expected))
	}
	for page := 1; page <= len(expected)+1; page++ {
		actual, total, err := repo.readPublicRecentDedupSnapshotPage(context.Background(), "snapshot-revision-001", page, 1)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if total != count {
			t.Fatalf("page %d total=%d want=%d", page, total, count)
		}
		if page > len(expected) {
			if len(actual) != 0 {
				t.Fatalf("page %d expected empty", page)
			}
			continue
		}
		if len(actual) != 1 {
			t.Fatalf("page %d wanted 1 row, got %d", page, len(actual))
		}
		prior := expected[page-1]
		got := actual[0]
		if got.ID != prior.ID || got.DuplicateCount != prior.DuplicateCount || !reflect.DeepEqual(got.DuplicateItemIDs, prior.DuplicateItemIDs) || !reflect.DeepEqual(got.DuplicateSources, prior.DuplicateSources) {
			t.Fatalf("page %d differs from existing duplicate rules: got=%+v expected=%+v", page, got, prior)
		}
		if got.FeedSource == nil {
			t.Fatalf("page %d failed to hydrate source", page)
		}
		if got.ID == fixtures[0].ID && got.FullTextHTML != heavy {
			t.Fatal("full content not hydrated")
		}
	}

	// The source revision is mandatory: never treat a stale projection as live.
	if _, _, err := repo.readPublicRecentDedupSnapshotPage(context.Background(), "", 1, 20); !errors.Is(err, ErrDedupSnapshotStale) {
		t.Fatalf("empty revision: %v", err)
	}
	if _, _, err := repo.readPublicRecentDedupSnapshotPage(context.Background(), "snapshot-revision-002", 1, 20); !errors.Is(err, ErrDedupSnapshotStale) {
		t.Fatalf("revision change: %v", err)
	}

	newItem := model.FeedItem{FeedSourceID: source.ID, Title: "New article after snapshot", GUID: "snapshot-new", Link: "https://example.org/new", PublishedAt: base.Add(6 * time.Minute)}
	if err := db.Create(&newItem).Error; err != nil {
		t.Fatal(err)
	}
	newCount, err := repo.publishPublicRecentDedupSnapshot(context.Background(), "snapshot-revision-002")
	if err != nil {
		t.Fatal(err)
	}
	if newCount != count+1 {
		t.Fatalf("new count=%d old=%d", newCount, count)
	}
	after, total, err := repo.readPublicRecentDedupSnapshotPage(context.Background(), "snapshot-revision-002", 1, 1)
	if err != nil || total != newCount || len(after) != 1 || after[0].ID != newItem.ID {
		t.Fatalf("atomic rebuild not visible: total=%d rows=%+v err=%v", total, after, err)
	}
	if _, _, err := repo.readPublicRecentDedupSnapshotPage(context.Background(), "snapshot-revision-001", 1, 1); !errors.Is(err, ErrDedupSnapshotStale) {
		t.Fatalf("old revision incorrectly served: %v", err)
	}
	var generations []feedDedupSnapshotRow
	if err := db.Where("feed_item_id = ?", fixtures[0].ID).Find(&generations).Error; err != nil {
		t.Fatal(err)
	}
	if len(generations) != 2 {
		t.Fatalf("older snapshot generation was removed during atomic switch; generations=%d", len(generations))
	}
}

func TestPublicRecentDedupSnapshotLargePageSeek(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fixtures := make([]model.FeedItem, 1050)
	for i := range fixtures {
		fixtures[i] = model.FeedItem{FeedSourceID: source.ID, GUID: fmt.Sprintf("snapshot-unique-guid-%04d", i),
			Title: uuid.NewString(), Link: fmt.Sprintf("https://example.net/entry/%06d", i), PublishedAt: base.Add(time.Duration(1100-i) * time.Second)}
	}
	if err := db.CreateInBatches(fixtures, 125).Error; err != nil {
		t.Fatal(err)
	}
	count, err := repo.publishPublicRecentDedupSnapshot(context.Background(), "revision-many-rows")
	if err != nil {
		t.Fatal(err)
	}
	if count != int64(len(fixtures)) {
		t.Fatalf("expected all unique stories, count=%d", count)
	}
	deepPage, total, err := repo.readPublicRecentDedupSnapshotPage(context.Background(), "revision-many-rows", 501, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1050 || len(deepPage) != 2 || deepPage[0].GUID != "snapshot-unique-guid-1000" || deepPage[1].GUID != "snapshot-unique-guid-1001" {
		t.Fatalf("deep page is incorrect: total=%d page=%+v", total, deepPage)
	}
	var rowCount int64
	if err := db.Model(&feedDedupSnapshotRow{}).Count(&rowCount).Error; err != nil || rowCount != 1050 {
		t.Fatalf("projected rows=%d err=%v", rowCount, err)
	}
}

func TestPublicRecentDedupSnapshotRejectsMissingHead(t *testing.T) {
	repo, _, _ := prepareDedupSnapshotTest(t)
	if _, _, err := repo.readPublicRecentDedupSnapshotPage(context.Background(), "revision-never-published", 1, 20); !errors.Is(err, ErrDedupSnapshotStale) {
		t.Fatalf("expected missing projection to be rejected, got %v", err)
	}
	if _, err := repo.publishPublicRecentDedupSnapshot(context.Background(), ""); err == nil {
		t.Fatal("blank revision must be rejected")
	}
}

func TestPublicRecentDedupSnapshotTableIndex(t *testing.T) {
	_, db, _ := prepareDedupSnapshotTest(t)
	var definition string
	if err := db.Raw("SELECT indexdef FROM pg_indexes WHERE schemaname=current_schema() AND tablename='feed_dedup_snapshot_rows' AND indexname='feed_dedup_snapshot_rows_pkey'").Scan(&definition).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(definition, "generation_id, \"position\"") {
		t.Fatalf("snapshot index must support deep page seeks, got %q", definition)
	}
}
