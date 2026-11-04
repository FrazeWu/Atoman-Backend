package feed

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
)

// Compare the projection to both real Service entry points. This verifies
// response DTOs for the only candidate query contract: public external RSS,
// recent ordering, hide_duplicates, and NO user filters. It does not change
// any API route or make filtered/subscribed/mixed requests eligible.
func TestSnapshotCOPYMatchesLegacyPublicAndExploreRequestShapes(t *testing.T) {
	s, db, _ := newFeedTestService(t)
	if err := db.Exec("DELETE FROM feed_item_reads").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM feed_items").Error; err != nil {
		t.Fatal(err)
	}
	clearFeedCanonicalBlogContent(t, db)
	if err := db.AutoMigrate(&feedDedupSnapshotHead{}, &feedDedupSnapshotGeneration{}, &feedDedupSnapshotRow{}, &feedDedupSnapshotMember{}); err != nil {
		t.Fatal(err)
	}
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	var one model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&one).Error; err != nil {
		t.Fatal(err)
	}
	two := model.FeedSource{SourceType: "external_rss", Title: "COPY request parity mirror", Hash: uuid.NewString(), RssURL: "https://rss.example.invalid/synthetic-mirror"}
	if err := db.Create(&two).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	ids := make([]uuid.UUID, 0, 28)
	for i := 0; i < 28; i++ {
		source := one.ID
		if i%3 == 0 {
			source = two.ID
		}
		link := fmt.Sprintf("https://rss.example.invalid/article/%02d", i)
		if i%7 == 1 {
			link = fmt.Sprintf("https://rss.example.invalid/article/%02d?utm_campaign=mirror", i-1)
		}
		title := fmt.Sprintf("Synthetic headline %s on independent topic %d", uuid.NewString(), i)
		if i == 0 {
			title = "Distinctive marker_search_needle_42 中文标题 🧪"
		}
		full := fmt.Sprintf("<p>synthetic private-free document %d</p>", i)
		record := model.FeedItem{FeedSourceID: source, GUID: fmt.Sprintf("copy-contract-%02d", i), Link: link, Title: title, FullTextHTML: full, ReaderHTML: fmt.Sprintf("<p>reader %d</p>", i), PublishedAt: base.Add(-time.Duration(i) * time.Minute)}
		if err := db.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, record.ID)
	}
	t.Setenv("FEED_SCALE_ISOLATED", "yes")
	t.Setenv("FEED_SNAPSHOT_COPY_EXPERIMENT", "yes")
	ctx := context.Background()
	count, err := s.repo.publishPublicRecentDedupSnapshotTracked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count < 15 || count > 28 {
		t.Fatalf("unexpected dedup group count %d", count)
	}
	pagesChecked := 0
	for _, size := range []int{1, 2, 3, 5, 10, 20, 50} {
		maxPage := int(count)/size + 2
		for page := 1; page <= maxPage; page++ {
			q := FeedQuery{Page: page, PageSize: size, HideDuplicates: true}
			pub, totalPub, err := s.GetPublicFeed(q)
			if err != nil {
				t.Fatalf("public size=%d page=%d: %v", size, page, err)
			}
			explore, totalExplore, err := s.GetExploreFeed(authctx.CurrentUser{}, q)
			if err != nil {
				t.Fatalf("explore size=%d page=%d: %v", size, page, err)
			}
			snap, totalSnap, err := s.repo.readPublicRecentDedupSnapshotPageTracked(ctx, page, size)
			if err != nil {
				t.Fatalf("projection size=%d page=%d: %v", size, page, err)
			}
			if totalPub != totalSnap || totalExplore != totalSnap || totalSnap != count || len(pub) != len(snap) || len(explore) != len(snap) {
				t.Fatalf("response length/count mismatch size=%d page=%d got pub=%d/%d explore=%d/%d snap=%d/%d", size, page, len(pub), totalPub, len(explore), totalExplore, len(snap), totalSnap)
			}
			for j, want := range snap {
				for _, candidate := range []struct {
					name string
					row  TimelineItemDTO
				}{{"public", pub[j]}, {"explore", explore[j]}} {
					item := candidate.row.FeedItem
					if item == nil {
						t.Fatalf("%s returned non-RSS item at %d/%d", candidate.name, size, page)
					}
					if item.ID != want.ID || item.Title != want.Title || item.Link != want.Link || !item.PublishedAt.Equal(want.PublishedAt) || item.FullTextHTML != want.FullTextHTML || item.ReaderHTML != want.ReaderHTML || item.DuplicateCount != want.DuplicateCount || !reflect.DeepEqual(item.DuplicateItemIDs, want.DuplicateItemIDs) || !reflect.DeepEqual(item.DuplicateSources, want.DuplicateSources) {
						t.Fatalf("%s mismatch size=%d page=%d row=%d got=%s expected=%s", candidate.name, size, page, j, item.ID, want.ID)
					}
					if item.FeedSource == nil || want.FeedSource == nil || item.FeedSource.ID != want.FeedSource.ID {
						t.Fatalf("source hydration mismatch size=%d page=%d", size, page)
					}
					if candidate.row.IsRead {
						t.Fatalf("guest %s unexpectedly read content", candidate.name)
					}
				}
			}
			pagesChecked++
		}
	}
	// These request shapes require per-user/search/source filtering that a
	// global RSS snapshot does not encode. Assert demonstrable discrepancy,
	// so that a future broad route switch cannot claim contract coverage.
	q := FeedQuery{Page: 1, PageSize: 50, HideDuplicates: true, Search: "marker_search_needle_42"}
	filtered, filteredTotal, err := s.GetPublicFeed(q)
	if err != nil {
		t.Fatal(err)
	}
	if filteredTotal >= count || len(filtered) == 0 {
		t.Fatalf("search did not narrow real public response: filtered=%d global=%d", filteredTotal, count)
	}
	// A single-source selection cannot use global cross-source dedup groups.
	q = FeedQuery{Page: 1, PageSize: 50, HideDuplicates: true, SourceID: two.ID}
	scoped, scopedTotal, scopedErr := s.GetPublicFeed(q)
	if scopedErr != nil {
		t.Fatal(scopedErr)
	}
	if scopedTotal == 0 || scopedTotal >= count || len(scoped) == 0 {
		t.Fatalf("source filter failed to narrow global projection: source=%d global=%d", scopedTotal, count)
	}
	readOnly := true
	q = FeedQuery{Page: 1, PageSize: 50, HideDuplicates: true, IsRead: &readOnly}
	unread, unreadTotal, err := s.GetExploreFeed(authctx.CurrentUser{}, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 0 || unreadTotal != 0 {
		t.Fatalf("guest read=true unexpectedly returned %d/%d entries", len(unread), unreadTotal)
	}
	// A source update makes the COPY projection stale; the fallback must
	// return the real public legacy response with the full original context.
	if err := db.Model(&model.FeedItem{}).Where("id=?", ids[0]).Update("title", "Published title changed after COPY").Error; err != nil {
		t.Fatal(err)
	}
	legacyCalled := 0
	legacy := func(_ context.Context, page, size int) ([]model.FeedItem, int64, error) {
		legacyCalled++
		items, total, err := s.GetPublicFeed(FeedQuery{Page: page, PageSize: size, HideDuplicates: true})
		if err != nil {
			return nil, 0, err
		}
		result := make([]model.FeedItem, 0, len(items))
		for _, row := range items {
			if row.FeedItem == nil {
				return nil, 0, fmt.Errorf("non-RSS response on scoped fallback")
			}
			result = append(result, *row.FeedItem)
		}
		return result, total, nil
	}
	var fallbackErr error
	old, total, err := s.repo.readPublicRecentDedupPageWithFallback(ctx, 1, 5, time.Second, legacy, func(e error) { fallbackErr = e })
	if err != nil || legacyCalled != 1 || !errors.Is(fallbackErr, ErrDedupSnapshotStale) || total != count || len(old) != 5 {
		t.Fatalf("stale COPY fallback failed count=%d size=%d called=%d err=%v fall=%v", total, len(old), legacyCalled, err, fallbackErr)
	}
	t.Logf("COPY_REQUEST_PARITY eligible_pages=%d first_count=%d public=true explore=true dto_fields=true source_hydration=true unsupported_search=true unsupported_source_filter=true unsupported_read_filter=true stale_fallback=true", pagesChecked, count)
}
