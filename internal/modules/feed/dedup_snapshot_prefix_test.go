package feed

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestPrefixRecentUniqueUsesIndexedFrontInsert(t *testing.T) {
	repo, db, source, first, _ := setUpIncrementalFeedTest(t)
	var headBefore feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&headBefore).Error; err != nil {
		t.Fatal(err)
	}
	latest := model.FeedItem{FeedSourceID: source.ID, GUID: "prefix-newest-unique", Title: "A brand new aerospace ecology discovery from satellites", Link: "https://science.example.net/fresh/001", PublishedAt: first.PublishedAt.Add(7 * time.Minute), FullTextHTML: "<p>newly published full text</p>"}
	if err := db.Create(&latest).Error; err != nil {
		t.Fatal(err)
	}
	result, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || result != PublicDedupRefreshIncremental {
		t.Fatalf("newest unique expected incremental: state=%s err=%v", result, err)
	}
	var after feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.GenerationID != headBefore.GenerationID || after.PositionBase != -1 || after.RawItemCount != 3 || after.Total != 3 || after.FirstFeedItemID != latest.ID {
		t.Fatalf("incremental front insert did not maintain keys: before=%+v after=%+v", headBefore, after)
	}
	assertIncrementalMatchesReference(t, repo)
	page, total, err := repo.readPublicRecentDedupSnapshotPageTracked(context.Background(), 1, 1)
	if err != nil || total != 3 || len(page) != 1 || page[0].FullTextHTML != latest.FullTextHTML {
		t.Fatalf("newest page not hydrated: page=%+v total=%d err=%v", page, total, err)
	}
}

func TestPrefixRecentBatchGroupsDuplicatesAndShiftsOnlyBase(t *testing.T) {
	repo, db, source, first, _ := setUpIncrementalFeedTest(t)
	batch := []model.FeedItem{
		{FeedSourceID: source.ID, GUID: "prefix-batch-head", Title: "A unique newly published article about modern robotics", Link: "https://prefix.example.org/robotics", PublishedAt: first.PublishedAt.Add(30 * time.Minute)},
		{FeedSourceID: source.ID, GUID: "prefix-batch-mirror", Title: "A completely different title from robotics", Link: "https://prefix.example.org/robotics?utm_campaign=mirror", PublishedAt: first.PublishedAt.Add(29 * time.Minute)},
		{FeedSourceID: source.ID, GUID: "prefix-batch-independent", Title: "A rare botanical mapping expedition in Antarctica", Link: "https://prefix.example.org/botany", PublishedAt: first.PublishedAt.Add(28 * time.Minute)},
	}
	if err := db.CreateInBatches(&batch, 100).Error; err != nil {
		t.Fatal(err)
	}
	result, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || result != PublicDedupRefreshIncremental {
		t.Fatalf("prefix batch must be incremental: state=%q err=%v", result, err)
	}
	var head feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&head).Error; err != nil {
		t.Fatal(err)
	}
	if head.PositionBase != -2 || head.Total != 4 || head.RawItemCount != 5 || head.FirstFeedItemID != batch[0].ID {
		t.Fatalf("incorrect batch shift/total: %+v", head)
	}
	assertIncrementalMatchesReference(t, repo)
	firstPage, _, err := repo.readPublicRecentDedupSnapshotPageTracked(context.Background(), 1, 1)
	if err != nil || firstPage[0].DuplicateCount != 2 || len(firstPage[0].DuplicateItemIDs) != 2 {
		t.Fatalf("new batch duplicate grouping lost: %+v err=%v", firstPage, err)
	}
}

func TestPrefixAfterTailAndTailAfterPrefixKeepContiguousPages(t *testing.T) {
	repo, db, source, first, oldest := setUpIncrementalFeedTest(t)
	latest := model.FeedItem{FeedSourceID: source.ID, GUID: "prefix-then-tail-first", Title: "An independent space laboratory announcement", Link: "https://prefix-tail.example.org/new", PublishedAt: first.PublishedAt.Add(time.Hour)}
	if err := db.Create(&latest).Error; err != nil {
		t.Fatal(err)
	}
	state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshIncremental {
		t.Fatalf("prefix phase: %q %v", state, err)
	}
	tail := model.FeedItem{FeedSourceID: source.ID, GUID: "prefix-then-tail-last", Title: "An ancient unrelated telescope survey", Link: "https://prefix-tail.example.org/old", PublishedAt: oldest.PublishedAt.Add(-100 * time.Hour)}
	if err := db.Create(&tail).Error; err != nil {
		t.Fatal(err)
	}
	state, err = repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshIncremental {
		t.Fatalf("tail after prefix: %q %v", state, err)
	}
	var head feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&head).Error; err != nil {
		t.Fatal(err)
	}
	if head.PositionBase != -1 || head.Total != 4 || head.RawItemCount != 4 || head.TailFeedItemID != tail.ID {
		t.Fatalf("contiguous page positions lost: %+v", head)
	}
	assertIncrementalMatchesReference(t, repo)
	newest2 := model.FeedItem{FeedSourceID: source.ID, GUID: "prefix-then-tail-next", Title: "A new independent story about coral reef satellites", Link: "https://prefix-tail.example.org/newest", PublishedAt: latest.PublishedAt.Add(time.Hour)}
	if err := db.Create(&newest2).Error; err != nil {
		t.Fatal(err)
	}
	state, err = repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshIncremental {
		t.Fatalf("second prefix phase: %q %v", state, err)
	}
	assertIncrementalMatchesReference(t, repo)
}

func TestPrefixRejectsMatchAgainstHiddenOldMember(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	primary := model.FeedItem{FeedSourceID: source.ID, GUID: "hidden-match-a", Title: "A very long headline about satellite climate research", Link: "https://origin.example.org/main", PublishedAt: base}
	hidden := model.FeedItem{FeedSourceID: source.ID, GUID: "hidden-match-b", Title: primary.Title, Link: "https://hidden.example.org/match", PublishedAt: base.Add(-time.Minute)}
	for _, item := range []*model.FeedItem{&primary, &hidden} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	if count, err := repo.publishPublicRecentDedupSnapshotTracked(context.Background()); err != nil || count != 1 {
		t.Fatalf("initial group count %d err=%v", count, err)
	}
	incoming := model.FeedItem{FeedSourceID: source.ID, GUID: "hidden-match-new", Title: "An unrelated article about independent ocean robotics", Link: hidden.Link, PublishedAt: base.Add(time.Hour)}
	if err := db.Create(&incoming).Error; err != nil {
		t.Fatal(err)
	}
	var headBefore feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&headBefore).Error; err != nil {
		t.Fatal(err)
	}
	state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshRebuilt {
		t.Fatalf("hidden-member match MUST rebuild rather than prepend: %q %v", state, err)
	}
	var headAfter feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&headAfter).Error; err != nil {
		t.Fatal(err)
	}
	if headAfter.GenerationID == headBefore.GenerationID {
		t.Fatal("unsafe reuse of old generation")
	}
	assertIncrementalMatchesReference(t, repo)
}

func TestPrefixFallbackOnIntermediateInsertAndBatchLimit(t *testing.T) {
	cases := []struct {
		name string
		put  func(t *testing.T, db *gorm.DB, source model.FeedSource, first, oldest model.FeedItem)
	}{
		{"insert in middle", func(t *testing.T, db *gorm.DB, source model.FeedSource, first, oldest model.FeedItem) {
			item := model.FeedItem{FeedSourceID: source.ID, GUID: "prefix-middle", Title: "A unique middle date story", Link: "https://prefix.example.org/middle", PublishedAt: oldest.PublishedAt.Add(time.Hour)}
			if err := db.Create(&item).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"more than bounded batch", func(t *testing.T, db *gorm.DB, source model.FeedSource, first, oldest model.FeedItem) {
			items := make([]model.FeedItem, maxSafePrefixBatch+1)
			for i := range items {
				items[i] = model.FeedItem{FeedSourceID: source.ID, GUID: fmt.Sprintf("prefix-overlimit-%d", i), Title: uuid.NewString(), Link: fmt.Sprintf("https://new.example.org/%d", i), PublishedAt: first.PublishedAt.Add(time.Duration(i+10) * time.Second)}
			}
			if err := db.CreateInBatches(items, 100).Error; err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, db, source, first, oldest := setUpIncrementalFeedTest(t)
			tc.put(t, db, source, first, oldest)
			state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
			if err != nil || state != PublicDedupRefreshRebuilt {
				t.Fatalf("unsupported prefix change must rebuild: %q %v", state, err)
			}
			assertIncrementalMatchesReference(t, repo)
		})
	}
}

func TestPrefixRejectsNullableOldFirstTimestamp(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	first := model.FeedItem{FeedSourceID: source.ID, GUID: "nullable-oldest", Title: "An article with unknown publication timestamp", Link: "https://null-date.example.org/article", PublishedAt: time.Now().UTC()}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE feed_items SET published_at = NULL WHERE id = ?", first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.publishPublicRecentDedupSnapshotTracked(context.Background()); err != nil {
		t.Fatal(err)
	}
	incoming := model.FeedItem{FeedSourceID: source.ID, GUID: "nullable-incoming", Title: "A distinct article with a valid publication timestamp", Link: "https://null-date.example.org/newest", PublishedAt: time.Now().UTC().Add(time.Hour)}
	if err := db.Create(&incoming).Error; err != nil {
		t.Fatal(err)
	}
	state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshRebuilt {
		t.Fatalf("NULL timestamp ordering must rebuild: state=%q err=%v", state, err)
	}
	assertIncrementalMatchesReference(t, repo)
}

func TestPrefixFallsBackIfRawMemberIndexIncomplete(t *testing.T) {
	repo, db, source, first, _ := setUpIncrementalFeedTest(t)
	var head feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&head).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("generation_id = ? AND feed_item_id = ?", head.GenerationID, first.ID).Delete(&feedDedupSnapshotMember{}).Error; err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{FeedSourceID: source.ID, GUID: "incomplete-index", Title: "An independent new publication from the latest research", Link: "https://incomplete.example.org/latest", PublishedAt: first.PublishedAt.Add(time.Hour)}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshRebuilt {
		t.Fatalf("partial member index must force full rebuild: %q %v", state, err)
	}
	assertIncrementalMatchesReference(t, repo)
}

func TestPrefixSameTimestampRespectsUUIDDescending(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		want     PublicDedupRefreshResult
	}{
		{"larger UUID first", "ffffffff-ffff-4fff-bfff-ffffffffffff", PublicDedupRefreshIncremental},
		{"smaller UUID follows", "00000000-0000-4000-8000-000000000001", PublicDedupRefreshRebuilt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, db, source, first, _ := setUpIncrementalFeedTest(t)
			incoming := model.FeedItem{Base: model.Base{ID: uuid.MustParse(tc.id)}, FeedSourceID: source.ID,
				GUID: "prefix-same-timestamp", Title: "An independent article appearing at an equal publication timestamp",
				Link: "https://prefix-uuid.example.org/unique", PublishedAt: first.PublishedAt}
			if err := db.Create(&incoming).Error; err != nil {
				t.Fatal(err)
			}
			state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
			if err != nil || state != tc.want {
				t.Fatalf("uuid tie sort: expected %q got %q err=%v", tc.want, state, err)
			}
			assertIncrementalMatchesReference(t, repo)
		})
	}
}

func TestPrefixLongURLCollisionGuard(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	longURL := "https://long-path.example.net/" + strings.Repeat("segment-with-very-long-stable-data/", 110)
	old := model.FeedItem{FeedSourceID: source.ID, GUID: "long-url-before", Title: "A headline concerning an unusual engineering project", Link: longURL, PublishedAt: time.Now().UTC()}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.publishPublicRecentDedupSnapshotTracked(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := model.FeedItem{FeedSourceID: source.ID, GUID: "long-url-after", Title: "An unrelated musical performance review", Link: longURL + "?utm_source=mirror", PublishedAt: old.PublishedAt.Add(time.Hour)}
	if err := db.Create(&next).Error; err != nil {
		t.Fatal(err)
	}
	state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshRebuilt {
		t.Fatalf("long URL equality must force rebuild: state=%q err=%v", state, err)
	}
	assertIncrementalMatchesReference(t, repo)
}

func TestPrefixUnicodeTitleConflictForcesFullRebuild(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := model.FeedItem{FeedSourceID: source.ID, GUID: "unicode-before", Title: "大语言模型安全评估体系与实时防御机制研究", Link: "https://chinese.example.org/old", PublishedAt: now}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.publishPublicRecentDedupSnapshotTracked(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := model.FeedItem{FeedSourceID: source.ID, GUID: "unicode-after", Title: "大语言模型安全评估体系与实时防御机制研究！", Link: "https://chinese.example.org/new", PublishedAt: now.Add(time.Minute)}
	if err := db.Create(&next).Error; err != nil {
		t.Fatal(err)
	}
	state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshRebuilt {
		t.Fatalf("unicode normalized title match must force rebuild: %q %v", state, err)
	}
	assertIncrementalMatchesReference(t, repo)
}

func TestTailRejectsNullPublishedAtAndRebuilds(t *testing.T) {
	repo, db, source, _, _ := setUpIncrementalFeedTest(t)
	inserted := model.FeedItem{FeedSourceID: source.ID, GUID: "null-date-tail", Title: "A publication with no known timestamp", Link: "https://null-tail.example.org/article"}
	if err := db.Omit("PublishedAt").Create(&inserted).Error; err != nil {
		t.Fatal(err)
	}
	// A NULL published_at in PostgreSQL is ordered before every non-NULL date.
	var missing bool
	if err := db.Raw("SELECT published_at IS NULL FROM feed_items WHERE id = ?", inserted.ID).Scan(&missing).Error; err != nil {
		t.Fatal(err)
	}
	if !missing {
		t.Fatal("test insertion must use database NULL timestamp")
	}
	state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{})
	if err != nil || state != PublicDedupRefreshRebuilt {
		t.Fatalf("NULL published time must force rebuild: %q %v", state, err)
	}
	assertIncrementalMatchesReference(t, repo)
}
