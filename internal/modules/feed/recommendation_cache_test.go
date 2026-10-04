package feed

import (
	"testing"

	"atoman/internal/modules/recommendation"
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
