package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"atoman/internal/model"
	legacyfeed "atoman/internal/service"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func setUpIncrementalFeedTest(t *testing.T) (*Repo, *gorm.DB, model.FeedSource, model.FeedItem, model.FeedItem) {
	t.Helper()
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	first := model.FeedItem{FeedSourceID: source.ID, GUID: "incremental-first", Title: "A completely individual first article on lunar mapping", Link: "https://example.org/lunar/001", PublishedAt: base.Add(2 * time.Hour), FullTextHTML: "<p>first article text</p>"}
	second := model.FeedItem{FeedSourceID: source.ID, GUID: "incremental-second", Title: "Mountain meteorology in a remote ocean valley", Link: "https://example.org/weather/002", PublishedAt: base}
	for _, item := range []*model.FeedItem{&first, &second} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	total, err := repo.publishPublicRecentDedupSnapshotTracked(context.Background())
	if err != nil || total != 2 {
		t.Fatalf("build initial projection: total=%d err=%v", total, err)
	}
	return repo, db, source, first, second
}

func assertIncrementalMatchesReference(t *testing.T, repo *Repo) {
	t.Helper()
	originals, err := repo.ListExploreFeedItemsForDuplicateScan("recent", FeedQuery{})
	if err != nil {
		t.Fatal(err)
	}
	legacyfeed.AnnotateDuplicateFeedItems(originals)
	expected := make([]model.FeedItem, 0, len(originals))
	for _, item := range originals {
		if !item.IsDuplicate {
			expected = append(expected, item)
		}
	}
	for page := 1; page <= len(expected)+1; page++ {
		actual, total, err := repo.readPublicRecentDedupSnapshotPageTracked(context.Background(), page, 1)
		if err != nil || total != int64(len(expected)) {
			t.Fatalf("page %d: total=%d expected=%d err=%v", page, total, len(expected), err)
		}
		if page > len(expected) {
			if len(actual) != 0 {
				t.Fatalf("page %d: expected no rows", page)
			}
			continue
		}
		if len(actual) != 1 {
			t.Fatalf("page %d: expected one row, got %d", page, len(actual))
		}
		got, want := actual[0], expected[page-1]
		if got.ID != want.ID || got.DuplicateCount != want.DuplicateCount ||
			!reflect.DeepEqual(got.DuplicateSources, want.DuplicateSources) || !reflect.DeepEqual(got.DuplicateItemIDs, want.DuplicateItemIDs) ||
			got.FullTextHTML == "" && want.FullTextHTML != "" {
			t.Fatalf("page %d mismatch: got (%s, %d, %+v, %+v), expected (%s, %d, %+v, %+v)",
				page, got.ID, got.DuplicateCount, got.DuplicateSources, got.DuplicateItemIDs,
				want.ID, want.DuplicateCount, want.DuplicateSources, want.DuplicateItemIDs)
		}
	}
}

func TestIncrementalOldestUniqueKeepsGenerationAndPages(t *testing.T) {
	repo, db, source, _, oldest := setUpIncrementalFeedTest(t)
	var headBefore feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&headBefore).Error; err != nil {
		t.Fatal(err)
	}
	var previous map[string]int64
	if err := json.Unmarshal(headBefore.SourceVersions, &previous); err != nil || previous[source.ID.String()] == 0 {
		t.Fatalf("checkpoint invalid: %+v, %v", previous, err)
	}
	tail := model.FeedItem{FeedSourceID: source.ID, GUID: "incremental-older-unique", Title: "A unique ancient archive from another planet", Link: "https://example.net/old/archive/003", PublishedAt: oldest.PublishedAt.Add(-10 * time.Hour), ReaderHTML: "<p>older entry body</p>"}
	if err := db.Create(&tail).Error; err != nil {
		t.Fatal(err)
	}
	result, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || result != PublicDedupRefreshIncremental {
		t.Fatalf("expected incremental tail append, got state=%q err=%v", result, err)
	}
	var headAfter feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&headAfter).Error; err != nil {
		t.Fatal(err)
	}
	if headBefore.GenerationID != headAfter.GenerationID || headAfter.Total != 3 || headAfter.TailFeedItemID != tail.ID {
		t.Fatalf("tail append should preserve projection generation and increase total; old=%+v new=%+v", headBefore, headAfter)
	}
	assertIncrementalMatchesReference(t, repo)
	last, total, err := repo.readPublicRecentDedupSnapshotPageTracked(context.Background(), 3, 1)
	if err != nil || total != 3 || len(last) != 1 || last[0].ReaderHTML != tail.ReaderHTML {
		t.Fatalf("final tail page not hydrated: %+v count=%d err=%v", last, total, err)
	}
}

func TestIncrementalOldestDuplicateMergesWithoutRebuild(t *testing.T) {
	repo, db, source, first, oldest := setUpIncrementalFeedTest(t)
	var headBefore feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&headBefore).Error; err != nil {
		t.Fatal(err)
	}
	mirror := model.FeedItem{FeedSourceID: source.ID, GUID: "incremental-older-duplicate", Title: "A completely unrelated title that must match URL", Link: first.Link + "?utm_campaign=mirror", PublishedAt: oldest.PublishedAt.Add(-200 * time.Hour)}
	if err := db.Create(&mirror).Error; err != nil {
		t.Fatal(err)
	}
	result, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || result != PublicDedupRefreshIncremental {
		t.Fatalf("expected URL duplicate increment: state=%q err=%v", result, err)
	}
	var headAfter feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&headAfter).Error; err != nil {
		t.Fatal(err)
	}
	if headBefore.GenerationID != headAfter.GenerationID || headAfter.Total != 2 || headAfter.TailFeedItemID != mirror.ID {
		t.Fatalf("merge should preserve generation/count and advance tail: %+v", headAfter)
	}
	assertIncrementalMatchesReference(t, repo)
	firstPage, _, err := repo.readPublicRecentDedupSnapshotPageTracked(context.Background(), 1, 1)
	if err != nil || firstPage[0].DuplicateCount != 2 || !reflect.DeepEqual(firstPage[0].DuplicateItemIDs, []uuid.UUID{first.ID, mirror.ID}) {
		t.Fatalf("canonical metadata missing second member: %+v %v", firstPage, err)
	}
}

func TestIncrementalUnsupportedChangesFallBackToFullRebuild(t *testing.T) {
	scenarios := []struct {
		name   string
		action func(t *testing.T, db *gorm.DB, source model.FeedSource, first, oldest model.FeedItem)
	}{
		{"newest insert", func(t *testing.T, db *gorm.DB, source model.FeedSource, first, oldest model.FeedItem) {
			item := model.FeedItem{FeedSourceID: source.ID, GUID: "newest-insert", Title: "First thing in the latest feed", Link: "https://example.org/newest", PublishedAt: first.PublishedAt.Add(time.Hour)}
			if err := db.Create(&item).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"batch insert", func(t *testing.T, db *gorm.DB, source model.FeedSource, first, oldest model.FeedItem) {
			items := []model.FeedItem{
				{FeedSourceID: source.ID, GUID: "batch-older-a", Title: "Ancient report from unrelated source alpha", Link: "https://example.org/a", PublishedAt: oldest.PublishedAt.Add(-2 * time.Hour)},
				{FeedSourceID: source.ID, GUID: "batch-older-b", Title: "Unrelated item from a distant galaxy beta", Link: "https://example.org/b", PublishedAt: oldest.PublishedAt.Add(-3 * time.Hour)},
			}
			if err := db.CreateInBatches(items, 100).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"article edit", func(t *testing.T, db *gorm.DB, source model.FeedSource, first, oldest model.FeedItem) {
			if err := db.Model(&first).Update("title", "Edited headline changes canonical matching").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"source hidden", func(t *testing.T, db *gorm.DB, source model.FeedSource, first, oldest model.FeedItem) {
			if err := db.Model(&source).Update("hidden", true).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"article deletion", func(t *testing.T, db *gorm.DB, source model.FeedSource, first, oldest model.FeedItem) {
			if err := db.Delete(&first).Error; err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			repo, db, source, first, oldest := setUpIncrementalFeedTest(t)
			var before feedDedupSnapshotHead
			if err := db.Where("scope = ?", publicRecentDedupScope).Take(&before).Error; err != nil {
				t.Fatal(err)
			}
			scenario.action(t, db, source, first, oldest)
			state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
			if err != nil || state != PublicDedupRefreshRebuilt {
				t.Fatalf("unsupported case %s should rebuild: state=%q err=%v", scenario.name, state, err)
			}
			var after feedDedupSnapshotHead
			if err := db.Where("scope = ?", publicRecentDedupScope).Take(&after).Error; err != nil {
				t.Fatal(err)
			}
			if before.GenerationID == after.GenerationID {
				t.Fatalf("unsupported case %s reused generation", scenario.name)
			}
			assertIncrementalMatchesReference(t, repo)
		})
	}
}

func TestOneAdvancedSourceGuard(t *testing.T) {
	cases := []struct {
		before, after map[string]int64
		valid         bool
	}{
		{map[string]int64{"a": 1}, map[string]int64{"a": 2}, true},
		{map[string]int64{"a": 1}, map[string]int64{"a": 3}, false},
		{map[string]int64{"a": 1, "b": 5}, map[string]int64{"a": 2, "b": 6}, false},
		{map[string]int64{"a": 1}, map[string]int64{}, false},
		{map[string]int64{}, map[string]int64{"a": 1}, true},
	}
	for i, tc := range cases {
		_, _, valid := oneAdvancedSource(tc.before, tc.after)
		if valid != tc.valid {
			t.Fatal(fmt.Sprintf("guard case %d failed", i))
		}
	}
}

func TestIncrementalOldestFuzzyTitleAndRepeatedAppend(t *testing.T) {
	repo, db, source, _, oldest := setUpIncrementalFeedTest(t)
	duplicate := model.FeedItem{
		FeedSourceID: source.ID, GUID: "incremental-fuzzy-duplicate",
		Title: oldest.Title + "!", Link: "https://other.example.org/mirror-weather",
		PublishedAt: oldest.PublishedAt.Add(-time.Minute),
	}
	if err := db.Create(&duplicate).Error; err != nil {
		t.Fatal(err)
	}
	result, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || result != PublicDedupRefreshIncremental {
		t.Fatalf("fuzzy duplicate should merge: %q %v", result, err)
	}
	assertIncrementalMatchesReference(t, repo)
	independent := model.FeedItem{
		FeedSourceID: source.ID, GUID: "incremental-after-fuzzy",
		Title: "Another completely independent older report about ocean geology",
		Link:  "https://independent.example.org/report", PublishedAt: duplicate.PublishedAt.Add(-100 * time.Hour),
	}
	if err := db.Create(&independent).Error; err != nil {
		t.Fatal(err)
	}
	result, err = repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || result != PublicDedupRefreshIncremental {
		t.Fatalf("second incremental append failed: %q %v", result, err)
	}
	assertIncrementalMatchesReference(t, repo)
}

func TestIncrementalCrossSourceDuplicatePreservesSortedSources(t *testing.T) {
	repo, db, original := prepareDedupSnapshotTest(t)
	mirrorSource := model.FeedSource{SourceType: "external_rss", Title: "Zeta mirror", Hash: "incremental-cross-source", RssURL: "https://mirror.example.org/rss"}
	if err := db.Create(&mirrorSource).Error; err != nil {
		t.Fatal(err)
	}
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	primary := model.FeedItem{FeedSourceID: original.ID, GUID: "incremental-primary-cross-source", Title: "Distinct original from primary source", Link: "https://example.org/canonical-cross", PublishedAt: base}
	if err := db.Create(&primary).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.publishPublicRecentDedupSnapshotTracked(context.Background()); err != nil {
		t.Fatal(err)
	}
	mirror := model.FeedItem{FeedSourceID: mirrorSource.ID, GUID: "incremental-mirror-cross-source", Title: "An unrelated mirror title", Link: primary.Link + "?utm_source=mirror", PublishedAt: base.Add(-100 * time.Hour)}
	if err := db.Create(&mirror).Error; err != nil {
		t.Fatal(err)
	}
	status, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || status != PublicDedupRefreshIncremental {
		t.Fatalf("cross-source mirror not incremental: %q %v", status, err)
	}
	assertIncrementalMatchesReference(t, repo)
	page, total, err := repo.readPublicRecentDedupSnapshotPageTracked(context.Background(), 1, 10)
	if err != nil || total != 1 || len(page) != 1 || len(page[0].DuplicateSources) != 2 {
		t.Fatalf("cross-source sources lost: %+v total=%d err=%v", page, total, err)
	}
}

func TestIncrementalEmptyProjectionCanAppendInitialArticle(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	total, err := repo.publishPublicRecentDedupSnapshotTracked(context.Background())
	if err != nil || total != 0 {
		t.Fatalf("expected empty snapshot: total=%d %v", total, err)
	}
	item := model.FeedItem{FeedSourceID: source.ID, Title: "Initial article created after an empty snapshot", Link: "https://example.org/first", GUID: "initial-incremental", PublishedAt: time.Now().UTC()}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshIncremental {
		t.Fatalf("first append into empty projection: %q %v", state, err)
	}
	assertIncrementalMatchesReference(t, repo)
}

func TestIncrementalSameTimestampUsesUUIDTieBreak(t *testing.T) {
	repo, db, source, _, oldest := setUpIncrementalFeedTest(t)
	smaller := model.FeedItem{
		Base:         model.Base{ID: uuid.MustParse("00000000-0000-4000-8000-000000000001")},
		FeedSourceID: source.ID, GUID: "incremental-tie-smaller",
		Title: "A unique timestamp tied article with smaller identifier",
		Link:  "https://unique.example.org/tie/smaller", PublishedAt: oldest.PublishedAt,
	}
	if err := db.Create(&smaller).Error; err != nil {
		t.Fatal(err)
	}
	state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshIncremental {
		t.Fatalf("smaller UUID must be last: %q %v", state, err)
	}
	assertIncrementalMatchesReference(t, repo)
}

func TestIncrementalTieWithLargerUUIDRequiresRebuild(t *testing.T) {
	repo, db, source, _, oldest := setUpIncrementalFeedTest(t)
	larger := model.FeedItem{
		Base:         model.Base{ID: uuid.MustParse("ffffffff-ffff-4fff-bfff-ffffffffffff")},
		FeedSourceID: source.ID, GUID: "incremental-tie-larger",
		Title: "Another article with same timestamp but larger identifier",
		Link:  "https://unique.example.org/tie/larger", PublishedAt: oldest.PublishedAt,
	}
	if err := db.Create(&larger).Error; err != nil {
		t.Fatal(err)
	}
	state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshRebuilt {
		t.Fatalf("larger UUID cannot append to end: %q %v", state, err)
	}
	assertIncrementalMatchesReference(t, repo)
}
