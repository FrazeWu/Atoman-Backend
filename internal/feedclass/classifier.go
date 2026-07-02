package feedclass

import (
	"net/url"
	"sort"
	"strings"
)

type RecentItem struct {
	Title         string
	Link          string
	EnclosureType string
}

type Source struct {
	Title       string
	RSSURL      string
	RecentItems []RecentItem
}

type normalizedSource struct {
	sourceText   string
	linkTexts    []string
	linkHosts    []string
	audioCount   int
	videoCount   int
	categoryHits map[string]int
}

type categoryRuleSet struct {
	strongHosts []string
	strongTerms []string
	hosts       []string
	terms       []string
}

type categoryScore struct {
	name  string
	score int
}

var categoryRuleSets = map[string]categoryRuleSet{
	"podcast": {
		strongHosts: []string{"xiaoyuzhoufm.com", "xiaoyuzhou.com", "justpod.fm", "typlog.io"},
		strongTerms: []string{"podcast", "播客", "小宇宙", "justpod", "typlog"},
		hosts:       []string{"podcasts.apple.com", "spotify.com"},
		terms:       []string{"podcaster", "podcast feed"},
	},
	"video": {
		strongHosts: []string{"youtube.com", "youtu.be", "bilibili.com", "b23.tv"},
		strongTerms: []string{"youtube", "bilibili"},
		hosts:       []string{"vimeo.com"},
		terms:       []string{"video", "视频", "录像"},
	},
	"forum": {
		strongHosts: []string{"v2ex.com", "linux.do", "nodeseek.com"},
		strongTerms: []string{"论坛", "v2ex", "linux.do", "nodeseek"},
		hosts:       []string{"discourse.org"},
		terms:       []string{"forum", "bbs", "discourse"},
	},
	"social": {
		strongHosts: []string{"x.com", "twitter.com", "reddit.com", "jike.info", "okjike.com", "zhihu.com", "weibo.com"},
		strongTerms: []string{"即刻", "知乎", "微博", "twitter", "reddit"},
		hosts:       []string{"weixin.sogou.com"},
		terms:       []string{"社交", "动态", "status", "timeline"},
	},
	"news": {
		strongHosts: []string{"36kr.com", "ftchinese.com", "nytimes.com", "cn.nytimes.com", "caixin.com", "jiemian.com", "huxiu.com", "gov.cn", "stats.gov.cn", "zaobao.com"},
		strongTerms: []string{"新闻", "快讯", "日报", "早报", "36kr", "36氪", "ftchinese", "nytimes", "caixin", "界面", "虎嗅"},
		hosts:       []string{"ithome.com", "engadget.com"},
		terms:       []string{"news", "资讯", "头条", "发布", "统计", "数据发布"},
	},
}

func Classify(source Source) string {
	normalized := normalizeSource(source)
	scores := scoreSourceCategories(normalized)
	return decideCategory(scores)
}

func normalizeSource(source Source) normalizedSource {
	result := normalizedSource{
		sourceText:   strings.ToLower(strings.TrimSpace(source.Title + " " + source.RSSURL)),
		linkTexts:    make([]string, 0, len(source.RecentItems)),
		linkHosts:    make([]string, 0, len(source.RecentItems)),
		categoryHits: map[string]int{},
	}

	for _, item := range source.RecentItems {
		enclosureType := strings.ToLower(strings.TrimSpace(item.EnclosureType))
		if strings.HasPrefix(enclosureType, "audio/") {
			result.audioCount++
			result.categoryHits["podcast"]++
		}
		if strings.HasPrefix(enclosureType, "video/") {
			result.videoCount++
			result.categoryHits["video"]++
		}

		host := normalizedURLHost(item.Link)
		if host != "" {
			result.linkHosts = append(result.linkHosts, host)
			result.linkTexts = append(result.linkTexts, host+" "+strings.ToLower(strings.TrimSpace(item.Title))+" "+strings.ToLower(strings.TrimSpace(item.Link)))
			for category, ruleSet := range categoryRuleSets {
				if hostMatchesAny(host, append(ruleSet.strongHosts, ruleSet.hosts...)...) {
					result.categoryHits[category]++
				}
			}
		}
	}

	return result
}

func scoreSourceCategories(source normalizedSource) map[string]int {
	scores := map[string]int{
		"blog":    0,
		"news":    0,
		"social":  0,
		"video":   0,
		"forum":   0,
		"podcast": 0,
	}

	if source.audioCount >= 2 {
		scores["podcast"] += 8
	}
	if source.videoCount >= 2 {
		scores["video"] += 8
	}

	for category, ruleSet := range categoryRuleSets {
		if containsHostToken(source.sourceText, ruleSet.strongHosts...) {
			scores[category] += 6
		}
		if containsAny(source.sourceText, ruleSet.strongTerms...) {
			scores[category] += 4
		}
		if containsAny(source.sourceText, ruleSet.terms...) {
			scores[category] += 2
		}
		for _, text := range source.linkTexts {
			if containsAny(text, ruleSet.strongTerms...) {
				scores[category] += 2
			}
			if containsAny(text, ruleSet.terms...) {
				scores[category]++
			}
		}
		if source.categoryHits[category] >= 2 {
			scores[category] += 4
		} else if source.categoryHits[category] == 1 {
			scores[category]++
		}
	}

	if source.categoryHits["social"] >= 2 {
		scores["social"] += 2
	}
	if source.categoryHits["social"] == 1 && containsAny(source.sourceText, "@") {
		scores["social"] += 2
	}

	if source.audioCount == 1 && scores["social"] >= 4 {
		scores["podcast"]--
	}
	if source.videoCount == 1 && scores["news"] >= 4 {
		scores["video"]--
	}
	if source.categoryHits["video"] == 1 && source.videoCount == 0 {
		scores["video"] -= 3
	}

	return scores
}

func decideCategory(scores map[string]int) string {
	ranked := make([]categoryScore, 0, len(scores)-1)
	for category, score := range scores {
		if category == "blog" {
			continue
		}
		ranked = append(ranked, categoryScore{name: category, score: score})
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].name < ranked[j].name
		}
		return ranked[i].score > ranked[j].score
	})

	if len(ranked) == 0 || ranked[0].score < 4 {
		return "blog"
	}
	if len(ranked) > 1 && ranked[0].score-ranked[1].score < 2 {
		return "blog"
	}
	return ranked[0].name
}

func normalizedURLHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func containsAny(value string, patterns ...string) bool {
	for _, pattern := range patterns {
		if strings.Contains(value, pattern) {
			return true
		}
	}
	return false
}

func hostMatchesAny(host string, domains ...string) bool {
	for _, domain := range domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func containsHostToken(value string, domains ...string) bool {
	for _, domain := range domains {
		if strings.Contains(value, "://"+domain+"/") ||
			strings.Contains(value, "://"+domain+"?") ||
			strings.Contains(value, "://www."+domain+"/") ||
			strings.Contains(value, "://www."+domain+"?") {
			return true
		}
	}
	return false
}
