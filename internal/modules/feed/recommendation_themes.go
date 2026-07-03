package feed

import "strings"

type recommendationThemeDefinition struct {
	ID          string
	Label       string
	Description string
	Keywords    []string
}

var recommendationThemesByCategory = map[string][]recommendationThemeDefinition{
	"blog": {
		{ID: "ai", Label: "AI", Description: "关注模型、工具、应用与研究动态。", Keywords: []string{"ai", "模型", "model", "agent", "llm", "openai", "人工智能"}},
		{ID: "design", Label: "设计", Description: "关注产品设计、视觉系统与交互表达。", Keywords: []string{"design", "设计", "ui", "ux", "视觉", "交互"}},
		{ID: "startup", Label: "创业", Description: "关注产品、团队、增长与商业判断。", Keywords: []string{"startup", "founder", "融资", "增长", "商业", "产品"}},
	},
	"news": {
		{ID: "world", Label: "全球", Description: "关注国际新闻、宏观变化与公共议题。", Keywords: []string{"world", "global", "国际", "全球", "headline", "宏观"}},
		{ID: "market", Label: "市场", Description: "关注市场动态、公司消息与财经新闻。", Keywords: []string{"market", "finance", "财经", "股市", "经济", "company"}},
	},
	"social": {
		{ID: "creators", Label: "创作者", Description: "关注创作者表达、社交平台动态与社区讨论。", Keywords: []string{"creator", "创作者", "twitter", "x.com", "jike", "zhihu", "社交"}},
	},
	"video": {
		{ID: "cinema", Label: "影像", Description: "关注影像内容、视频作品与发行动态。", Keywords: []string{"video", "电影", "video", "trailer", "cinema", "影像"}},
	},
	"forum": {
		{ID: "community", Label: "社区", Description: "关注论坛讨论、问答与社区趋势。", Keywords: []string{"forum", "社区", "问答", "discussion", "bbs", "v2ex"}},
	},
	"podcast": {
		{ID: "startup", Label: "创业", Description: "关注创业者访谈、产品经营与团队故事。", Keywords: []string{"startup", "founder", "创业", "访谈", "operator", "funding"}},
		{ID: "technology", Label: "科技", Description: "关注科技行业观察、工具与趋势讨论。", Keywords: []string{"tech", "technology", "科技", "工具", "软件", "ai"}},
	},
}

func listRecommendationThemes(category string) []RecommendationThemeDTO {
	normalizedCategory := normalizeSourceCategory(category)
	definitions := recommendationThemesByCategory[normalizedCategory]
	items := make([]RecommendationThemeDTO, 0, len(definitions))
	for _, definition := range definitions {
		items = append(items, RecommendationThemeDTO{
			ID:          definition.ID,
			Label:       definition.Label,
			Description: definition.Description,
		})
	}
	return items
}

func findRecommendationTheme(category string, themeID string) (recommendationThemeDefinition, bool) {
	normalizedCategory := normalizeSourceCategory(category)
	normalizedThemeID := strings.TrimSpace(strings.ToLower(themeID))
	for _, definition := range recommendationThemesByCategory[normalizedCategory] {
		if definition.ID == normalizedThemeID {
			return definition, true
		}
	}
	return recommendationThemeDefinition{}, false
}
