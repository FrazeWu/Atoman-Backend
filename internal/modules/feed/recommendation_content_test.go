package feed

import (
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/recommendation"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestNormalizeArticleQualityPrefersStructuredContent(t *testing.T) {
	structuredContent := "# 完整分析\n\n"
	for index := 0; index < 8; index++ {
		structuredContent += "这一段提供论证、例子和结论，帮助读者理解问题的背景与影响。\n\n"
	}

	structured := RecommendationArticlePostRow{
		Content:       structuredContent,
		ContentLength: int64(len(structuredContent)),
		HasSummary:    true,
	}
	weakContent := RecommendationArticlePostRow{
		Content:       strings.Repeat("简短更新。", 60),
		ContentLength: int64(len(strings.Repeat("简短更新。", 60))),
	}

	if normalizeArticleQuality(structured) <= normalizeArticleQuality(weakContent) {
		t.Fatalf("structured article must rank above weak article: structured=%.3f weak=%.3f", normalizeArticleQuality(structured), normalizeArticleQuality(weakContent))
	}
}

func TestArticleContentQualityPenalizesLinkHeavyContent(t *testing.T) {
	structuredContent := "# 研究笔记\n\n"
	for index := 0; index < 8; index++ {
		structuredContent += "这一段提供完整论述与具体例证，避免把文章退化为一组外部链接。\n\n"
	}
	linkHeavyContent := strings.Repeat("[资料](https://example.com/reference)\n", 80)

	_, structuredScore := articleContentQualitySignals(structuredContent, int64(len(structuredContent)))
	_, linkHeavyScore := articleContentQualitySignals(linkHeavyContent, int64(len(linkHeavyContent)))
	if structuredScore <= linkHeavyScore {
		t.Fatalf("structured content must score above a link list: structured=%.3f links=%.3f", structuredScore, linkHeavyScore)
	}
}

func TestRecommendArticlesSeparatesHotFromFeaturedRanking(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.FeedSource{},
		&model.FeedItem{},
		&model.FeedItemRead{},
		&model.FeedItemStar{},
	)

	now := time.Now().UTC()
	featuredSource := model.FeedSource{
		SourceType: "external_rss",
		Hash:       "featured-source-" + uuid.NewString(),
		Title:      "Featured source",
		Category:   "blog",
	}
	hotSource := model.FeedSource{
		SourceType: "external_rss",
		Hash:       "hot-source-" + uuid.NewString(),
		Title:      "Hot source",
		Category:   "blog",
	}
	if err := db.Create(&featuredSource).Error; err != nil {
		t.Fatalf("create featured source: %v", err)
	}
	if err := db.Create(&hotSource).Error; err != nil {
		t.Fatalf("create hot source: %v", err)
	}

	featuredItem := model.FeedItem{
		FeedSourceID:       featuredSource.ID,
		GUID:               "featured-item",
		Title:              "深度专题",
		Summary:            strings.Repeat("完整的专题内容。", 30),
		Link:               "https://example.com/featured",
		ReaderQualityScore: 100,
		FullTextWordCount:  1200,
		PublishedAt:        now.Add(-6 * 24 * time.Hour),
		FetchedAt:          now,
	}
	hotItem := model.FeedItem{
		FeedSourceID:       hotSource.ID,
		GUID:               "hot-item",
		Title:              "刚刚发布的更新",
		Summary:            strings.Repeat("及时的更新内容。", 30),
		Link:               "https://example.com/hot",
		ReaderQualityScore: 70,
		FullTextWordCount:  800,
		PublishedAt:        now.Add(-time.Hour),
		FetchedAt:          now,
	}
	if err := db.Create(&featuredItem).Error; err != nil {
		t.Fatalf("create featured feed item: %v", err)
	}
	if err := db.Create(&hotItem).Error; err != nil {
		t.Fatalf("create hot feed item: %v", err)
	}

	hotItems, _, err := NewService(db).RecommendArticlesByMode(recommendation.ModeHot, "blog", "", "", "", 1, 20)
	if err != nil {
		t.Fatalf("recommend hot articles: %v", err)
	}
	if len(hotItems) < 2 || hotItems[0].ID != hotItem.ID.String() {
		t.Fatalf("expected newest trending article first for hot mode, got %+v", hotItems)
	}

	featuredItems, _, err := NewService(db).RecommendArticlesByMode(recommendation.ModeFeatured, "blog", "", "", "", 1, 20)
	if err != nil {
		t.Fatalf("recommend featured articles: %v", err)
	}
	if len(featuredItems) < 2 || featuredItems[0].ID != featuredItem.ID.String() {
		t.Fatalf("expected higher-quality article first for featured mode, got %+v", featuredItems)
	}
}
