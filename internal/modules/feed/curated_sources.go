package feed

import "strings"

// curatedSourceCatalog 是人工确认的 RSS 来源标题目录。语言归属以此目录为准，
// 不再相信历史抓取留下的 source.language_code。
var curatedSourceCatalog = map[string][]string{
	"zh": {
		"人人都是产品经理", "极客公园", "机核", "少数派", "阮一峰的网络日志", "编程随想的博客",
		"Mikan Project", "美团技术团队", "V2EX 最新", "爱范儿", "界面 RSS 订阅", "Wenson的隨筆網站",
	},
	"en": {
		"CISA News", "Dev.to", "Google Developers Blog", "The Brothers' Weekly Feed", "OpenClaw Commits", "Core77",
		"500px", "MP Social", "Wallpaper", "Hacker News Ask", "Hacker News Show", "Hacker News 最新",
	},
	"ja": {
		"Business Insider Japan", "10＋1 website 特集", "10＋1 website RSS", "10＋1 建筑资讯", "architecturephoto.net", "AI Will",
		"JDN", "AXIS", "AKICHIATLAS", "基本読書", "PANDA Chronicle", "ÉKRITS",
	},
	"de": {"Design made in Germany"},
	"fr": {"Fubiz Media"},
	"ko": {"모기", "mONSTER dESIGN bLOG"},
	"ru": {"Андрей Гордеев"},
}

func curatedSourceTitles(languageCode string) []string {
	languageCode = strings.TrimSpace(strings.ToLower(languageCode))
	if languageCode != "" {
		return append([]string(nil), curatedSourceCatalog[languageCode]...)
	}

	seen := make(map[string]struct{})
	titles := make([]string, 0, recommendationFeaturedSourceLimit)
	for _, entries := range curatedSourceCatalog {
		for _, title := range entries {
			key := strings.ToLower(strings.TrimSpace(title))
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			titles = append(titles, title)
		}
	}
	return titles
}
