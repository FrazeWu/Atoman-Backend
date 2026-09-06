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

func TestCuratedRecommendationsOnlyUseTwelveTopSourcesForLanguage(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.FeedSource{},
		&model.FeedItem{},
		&model.Subscription{},
	)

	now := time.Now().UTC()
	selectedItemIDs := make(map[string]struct{}, 12)
	for index := 0; index < 12; index++ {
		source := model.FeedSource{
			SourceType:   "external_rss",
			Hash:         "selected-source-" + uuid.NewString(),
			Title:        curatedSourceCatalog["zh"][index],
			Category:     "blog",
			LanguageCode: "zh",
			RssURL:       "https://example.com/selected-source/" + uuid.NewString(),
		}
		if err := db.Create(&source).Error; err != nil {
			t.Fatalf("create selected source: %v", err)
		}
		if err := db.Create(&model.Subscription{UserID: uuid.New(), FeedSourceID: source.ID}).Error; err != nil {
			t.Fatalf("create selected subscription: %v", err)
		}
		item := model.FeedItem{
			FeedSourceID: source.ID, GUID: "selected-item-" + uuid.NewString(), Title: "Selected item",
			Summary: strings.Repeat("完整内容。", 80), Link: "https://example.com/selected/" + source.ID.String(),
			ReaderQualityScore: 90, FullTextWordCount: 1200, LanguageCode: "zh", PublishedAt: now, FetchedAt: now,
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create selected item: %v", err)
		}
		selectedItemIDs[item.ID.String()] = struct{}{}
	}

	unselectedSource := model.FeedSource{SourceType: "external_rss", Hash: "unselected-source-" + uuid.NewString(), Title: "Unselected source", Category: "blog", LanguageCode: "zh"}
	if err := db.Create(&unselectedSource).Error; err != nil {
		t.Fatalf("create unselected source: %v", err)
	}
	unselectedItem := model.FeedItem{
		FeedSourceID: unselectedSource.ID, GUID: "unselected-item", Title: "Unselected item",
		Summary: strings.Repeat("完整内容。", 80), Link: "https://example.com/unselected",
		ReaderQualityScore: 100, FullTextWordCount: 1200, LanguageCode: "zh", PublishedAt: now, FetchedAt: now,
	}
	if err := db.Create(&unselectedItem).Error; err != nil {
		t.Fatalf("create unselected item: %v", err)
	}

	for _, mode := range []recommendation.Mode{recommendation.ModeFeatured, recommendation.ModeRandom} {
		items, _, err := NewService(db).RecommendArticlesByMode(mode, "blog", "", "zh", "", 1, 20)
		if err != nil {
			t.Fatalf("recommend %s articles: %v", mode, err)
		}
		if len(items) != 12 {
			t.Fatalf("expected 12 curated items for %s, got %#v", mode, items)
		}
		for _, item := range items {
			if _, ok := selectedItemIDs[item.ID]; !ok || item.ID == unselectedItem.ID.String() {
				t.Fatalf("%s recommendations must exclude items outside the top 12 sources: %#v", mode, items)
			}
		}
	}
}

func TestCuratedRecommendationsUseEditorialSourcesWithCorrectedLanguage(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.FeedSource{},
		&model.FeedItem{},
	)

	now := time.Now().UTC()
	trustedSource := model.FeedSource{
		SourceType:   "external_rss",
		Hash:         "curated-trusted-" + uuid.NewString(),
		Title:        "少数派",
		Category:     "blog",
		LanguageCode: "en", // 编辑目录而非旧数据的语言标记决定归属。
		RssURL:       "https://sspai.example.com/feed",
		CanonicalURL: "https://sspai.example.com",
	}
	duplicateTrustedSource := model.FeedSource{
		SourceType:   "external_rss",
		Hash:         "curated-trusted-duplicate-" + uuid.NewString(),
		Title:        "少数派",
		Category:     "blog",
		LanguageCode: "zh",
		RssURL:       "https://sspai.example.com/duplicate-feed",
		CanonicalURL: "https://sspai.example.com",
	}
	untrustedSource := model.FeedSource{
		SourceType:   "external_rss",
		Hash:         "curated-untrusted-" + uuid.NewString(),
		Title:        "Golang Weekly",
		Category:     "blog",
		LanguageCode: "pt",
		RssURL:       "https://golangweekly.example.com/feed",
	}
	for _, source := range []*model.FeedSource{&trustedSource, &duplicateTrustedSource, &untrustedSource} {
		if err := db.Create(source).Error; err != nil {
			t.Fatalf("create source: %v", err)
		}
	}
	trustedItem := model.FeedItem{
		FeedSourceID: trustedSource.ID, GUID: "curated-trusted-item", Title: "可信中文文章",
		Summary: strings.Repeat("完整内容。", 80), Link: "https://example.com/trusted",
		ReaderQualityScore: 90, FullTextWordCount: 1200, LanguageCode: "zh", PublishedAt: now, FetchedAt: now,
	}
	duplicateTrustedItem := model.FeedItem{
		FeedSourceID: duplicateTrustedSource.ID, GUID: "curated-trusted-duplicate-item", Title: "重复来源文章",
		Summary: strings.Repeat("完整内容。", 80), Link: "https://example.com/trusted-duplicate",
		ReaderQualityScore: 90, FullTextWordCount: 1200, LanguageCode: "zh", PublishedAt: now, FetchedAt: now,
	}
	untrustedItem := model.FeedItem{
		FeedSourceID: untrustedSource.ID, GUID: "curated-untrusted-item", Title: "误标来源文章",
		Summary: strings.Repeat("Complete content. ", 80), Link: "https://example.com/untrusted",
		ReaderQualityScore: 90, FullTextWordCount: 1200, LanguageCode: "pt", PublishedAt: now, FetchedAt: now,
	}
	for _, item := range []*model.FeedItem{&trustedItem, &duplicateTrustedItem, &untrustedItem} {
		if err := db.Create(item).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}

	items, _, err := NewService(db).RecommendArticlesByMode(recommendation.ModeFeatured, "blog", "", "zh", "", 1, 20)
	if err != nil {
		t.Fatalf("recommend curated Chinese articles: %v", err)
	}
	if len(items) != 1 || (items[0].ID != trustedItem.ID.String() && items[0].ID != duplicateTrustedItem.ID.String()) {
		t.Fatalf("expected only the editorial Chinese source, got %#v", items)
	}

	items, _, err = NewService(db).RecommendArticlesByMode(recommendation.ModeFeatured, "blog", "", "pt", "", 1, 20)
	if err != nil {
		t.Fatalf("recommend curated Portuguese articles: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("mislabelled sources must not fill the Portuguese curated pool: %#v", items)
	}
}
