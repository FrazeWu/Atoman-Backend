package feedclass

import (
	"net/url"
	"strings"
)

type RecentItem struct {
	Title         string
	Link          string
	EnclosureType string
}

type Source struct {
	Title     string
	RSSURL    string
	RecentItems []RecentItem
}

func Classify(source Source) string {
	sourceText := strings.ToLower(strings.TrimSpace(source.Title + " " + source.RSSURL))
	linkHosts := make([]string, 0, len(source.RecentItems))
	linkTexts := make([]string, 0, len(source.RecentItems))
	audioCount := 0
	videoCount := 0
	videoHostCount := 0

	for _, item := range source.RecentItems {
		enclosureType := strings.ToLower(strings.TrimSpace(item.EnclosureType))
		if strings.HasPrefix(enclosureType, "audio/") {
			audioCount++
		}
		if strings.HasPrefix(enclosureType, "video/") {
			videoCount++
		}

		host := normalizedURLHost(item.Link)
		if host != "" {
			linkHosts = append(linkHosts, host)
			linkTexts = append(linkTexts, host+" "+strings.ToLower(strings.TrimSpace(item.Title))+" "+strings.ToLower(strings.TrimSpace(item.Link)))
			if hostMatchesAny(host, "youtube.com", "youtu.be", "bilibili.com", "b23.tv", "vimeo.com") {
				videoHostCount++
			}
		}
	}

	if audioCount >= 2 {
		return "podcast"
	}
	if videoCount >= 2 {
		return "video"
	}

	for _, text := range append([]string{sourceText}, linkTexts...) {
		if containsAny(text, "xiaoyuzhou", "podcast", "播客", "justpod", "typlog.io") {
			return "podcast"
		}
	}

	for _, host := range linkHosts {
		if hostMatchesAny(host, "x.com", "twitter.com", "zhihu.com", "jike.info", "okjike.com", "reddit.com", "weibo.com", "weixin.sogou.com") {
			return "social"
		}
	}
	for _, text := range append([]string{sourceText}, linkTexts...) {
		if containsAny(text, "twitter", "zhihu", "jike", "reddit", "weibo", "即刻", "知乎", "公众号") || tokenMatchesAny(text, "x.com") {
			return "social"
		}
	}

	if videoHostCount >= 2 {
		return "video"
	}
	if containsAny(sourceText, "youtube", "bilibili", "vimeo", "视频") {
		return "video"
	}

	for _, host := range linkHosts {
		if hostMatchesAny(host, "v2ex.com", "nodeseek.com", "linux.do") || containsAny(host, "discourse", "bbs") {
			return "forum"
		}
	}
	for _, text := range append([]string{sourceText}, linkTexts...) {
		if containsAny(text, "forum", "bbs", "discourse", "v2ex", "nodeseek", "linux.do", "论坛") {
			return "forum"
		}
	}

	for _, host := range linkHosts {
		if hostMatchesAny(host, "36kr.com", "ftchinese.com", "nytimes.com", "cn.nytimes.com", "gov.cn", "stats.gov.cn", "caixin.com", "jiemian.com", "huxiu.com", "zaobao.com", "engadget.com", "anthropic.com", "deeplearning.ai", "paper.people.com.cn", "elastic.co", "ithome.com", "japandesign.ne.jp") {
			return "news"
		}
	}
	for _, text := range append([]string{sourceText}, linkTexts...) {
		if containsAny(text, "news", "新闻", "36kr", "36氪", "ftchinese", "nytimes", "gov.cn", "stats.gov", "统计", "数据发布", "caixin", "jiemian", "huxiu", "早报") {
			return "news"
		}
	}

	return "blog"
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

func tokenMatchesAny(value string, tokens ...string) bool {
	for _, token := range tokens {
		if value == token ||
			strings.Contains(value, "://"+token+"/") ||
			strings.Contains(value, "://"+token+"?") ||
			strings.Contains(value, " "+token+" ") ||
			strings.HasSuffix(value, " "+token) ||
			strings.HasPrefix(value, token+" ") {
			return true
		}
	}
	return false
}
