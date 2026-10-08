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
	for _, includeText := range []bool{true, false} {
		got, err := NewRepo(db).ListRecommendationArticleFeedItems(includeText, "blog", now.Add(-24*time.Hour), nil, "zh", "", []uuid.UUID{sources[0].ID, sources[1].ID}, 2)
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
