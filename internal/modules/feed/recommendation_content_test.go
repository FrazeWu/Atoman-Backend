package feed

import (
	"strings"
	"testing"
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
