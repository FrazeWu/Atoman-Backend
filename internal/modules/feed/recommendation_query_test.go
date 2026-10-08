package feed

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/google/uuid"
)

func TestRecommendationCandidatesPreserveGlobalOrderAndText(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	now := time.Now().UTC()
	sources := []model.FeedSource{
		{SourceType: "external_rss", Hash: uuid.NewString(), Title: "first", Category: "blog"},
		{SourceType: "external_rss", Hash: uuid.NewString(), Title: "second", Category: "blog"},
	}
	for i := range sources {
		if err := db.Create(&sources[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	items := []model.FeedItem{
		{FeedSourceID: sources[0].ID, GUID: "old", Title: "Older article", PublishedAt: now.Add(-time.Hour), ReaderQualityScore: 80, LanguageCode: "zh"},
		{FeedSourceID: sources[1].ID, GUID: "new", Title: "Newest article", Summary: "Article summary", Link: "https://example.com/article", ReaderHTML: "<p>Body</p>", PublishedAt: now, ReaderQualityScore: 80, LanguageCode: "zh"},
		{FeedSourceID: sources[0].ID, GUID: "middle", Title: "Middle article", PublishedAt: now.Add(-time.Minute), ReaderQualityScore: 80, LanguageCode: "zh"},
		{FeedSourceID: sources[1].ID, GUID: "video", Title: "Excluded video", PublishedAt: now.Add(time.Hour), ReaderQualityScore: 80, LanguageCode: "zh", EnclosureType: "video/mp4"},
		{FeedSourceID: sources[1].ID, GUID: "english", Title: "Excluded language", PublishedAt: now.Add(time.Hour), ReaderQualityScore: 80, LanguageCode: "en"},
		{FeedSourceID: sources[1].ID, GUID: "low", Title: "Excluded quality", PublishedAt: now.Add(time.Hour), ReaderQualityScore: 10, LanguageCode: "zh"},
	}
	for i := range items {
		if err := db.Create(&items[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, sourceIDs := range [][]uuid.UUID{nil, {sources[0].ID, sources[1].ID}} {
		for _, includeText := range []bool{true, false} {
			got, err := NewRepo(db).ListRecommendationArticleFeedItems(includeText, "blog", now.Add(-24*time.Hour), nil, "zh", "", sourceIDs, 2)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0].ID != items[1].ID || got[1].ID != items[2].ID {
				t.Fatalf("global candidates changed: %+v", got)
			}
			if got[0].Link != items[1].Link || !got[0].HasFullText {
				t.Fatalf("lost link or body signal: %+v", got[0])
			}
			if includeText && (got[0].Title != items[1].Title || got[0].Summary != items[1].Summary) {
				t.Fatalf("lost title or summary: %+v", got[0])
			}
		}
	}
}

func TestRecommendationCandidatesKeepCategoryFallbackAndEnclosureOverrides(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	now := time.Now().UTC()
	expected := map[string]map[uuid.UUID]bool{}
	for _, fixture := range []struct{ category, enclosure, want string }{
		{"blog", "", "blog"}, {"BLOG", "", "blog"}, {"other", "", "blog"}, {"", "", "blog"},
		{"news", "", "news"}, {"NEWS", "", "news"}, {"social", "", "social"},
		{"video", "", "video"}, {"forum", "", "forum"}, {"podcast", "", "podcast"},
		{"blog", "video/mp4", "video"}, {"news", "audio/mpeg", "podcast"},
	} {
		source := model.FeedSource{SourceType: "external_rss", Hash: uuid.NewString(), Title: "Category fixture", Category: fixture.category}
		if err := db.Create(&source).Error; err != nil {
			t.Fatal(err)
		}
		// 空分类也必须保留“归入博客”的现有语义，不依赖模型默认值。
		if err := db.Model(&source).Update("category", fixture.category).Error; err != nil {
			t.Fatal(err)
		}
		item := model.FeedItem{FeedSourceID: source.ID, GUID: uuid.NewString(), PublishedAt: now, ReaderQualityScore: 80, EnclosureType: fixture.enclosure}
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
		if expected[fixture.want] == nil {
			expected[fixture.want] = map[uuid.UUID]bool{}
		}
		expected[fixture.want][item.ID] = true
	}
	for category, want := range expected {
		got, err := NewRepo(db).ListRecommendationArticleFeedItems(false, category, now.Add(-time.Hour), nil, "", "", nil, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("category %s: got %d candidates, want %d", category, len(got), len(want))
		}
		for _, item := range got {
			if !want[item.ID] {
				t.Fatalf("category %s: unexpected candidate %s", category, item.ID)
			}
		}
	}
}
