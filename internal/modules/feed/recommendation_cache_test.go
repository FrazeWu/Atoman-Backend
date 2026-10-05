package feed

import (
	"testing"
	"time"

	"atoman/internal/modules/recommendation"
	"github.com/google/uuid"
)

func TestRecommendationCacheKeyIncludesPublicFilters(t *testing.T) {
	base := recommendationCacheKey("articles", recommendation.ModeFeatured, "blog", "ai", "zh", "", 1, 20)
	variants := []string{
		recommendationCacheKey("articles", recommendation.ModeFeatured, "news", "ai", "zh", "", 1, 20),
		recommendationCacheKey("articles", recommendation.ModeFeatured, "blog", "tech", "zh", "", 1, 20),
		recommendationCacheKey("articles", recommendation.ModeFeatured, "blog", "ai", "en", "", 1, 20),
		recommendationCacheKey("articles", recommendation.ModeFeatured, "blog", "ai", "zh", "", 2, 20),
	}

	for _, variant := range variants {
		if variant == base {
			t.Fatalf("cache key does not include all recommendation filters: %q", base)
		}
	}
}

func TestCuratedSourceCacheKeyIncludesLanguage(t *testing.T) {
	zh := curatedSourceCacheKey("zh")
	en := curatedSourceCacheKey("en")
	if zh == en {
		t.Fatalf("curated source cache key must include language: %q", zh)
	}
}

func TestCuratedSourceCacheEntryRoundTrip(t *testing.T) {
	publishedAt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	want := curatedSourceCacheEntry{Sources: []ExploreSourceRow{{
		ID:              uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Title:           "Example",
		LanguageCode:    "zh",
		LastPublishedAt: &publishedAt,
		RecentItems:     []ExploreSourceRecentItem{{Title: "Recent"}},
	}}}

	payload, err := marshalCuratedSourceCacheEntry(want)
	if err != nil {
		t.Fatalf("marshal curated source cache entry: %v", err)
	}
	got, err := unmarshalCuratedSourceCacheEntry(payload)
	if err != nil {
		t.Fatalf("unmarshal curated source cache entry: %v", err)
	}
	if len(got.Sources) != 1 || got.Sources[0].Title != "Example" || got.Sources[0].LastPublishedAt == nil || len(got.Sources[0].RecentItems) != 1 {
		t.Fatalf("curated source cache round trip mismatch: %#v", got)
	}
}
func TestRecommendationCacheEntryRoundTrip(t *testing.T) {
	want := recommendationCacheEntry{
		Items: []RecommendationItemDTO{{ID: "item-1", Title: "Cached item"}},
		Total: 1,
	}

	payload, err := marshalRecommendationCacheEntry(want)
	if err != nil {
		t.Fatalf("marshal cache entry: %v", err)
	}
	got, err := unmarshalRecommendationCacheEntry(payload)
	if err != nil {
		t.Fatalf("unmarshal cache entry: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "item-1" || got.Total != 1 {
		t.Fatalf("cache entry round trip mismatch: %#v", got)
	}
}
