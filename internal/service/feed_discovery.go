package service

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var feedAlternateLinkPattern = regexp.MustCompile(`(?is)<link\b[^>]*>`)
var feedRelAttrPattern = regexp.MustCompile(`(?is)\brel\s*=\s*(?:"([^"]*)"|'([^']*)')`)
var feedTypeAttrPattern = regexp.MustCompile(`(?is)\btype\s*=\s*(?:"([^"]*)"|'([^']*)')`)
var feedHrefAttrPattern = regexp.MustCompile(`(?is)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)')`)
var feedTitleAttrPattern = regexp.MustCompile(`(?is)\btitle\s*=\s*(?:"([^"]*)"|'([^']*)')`)

type FeedDiscoveryCandidate struct {
	Title     string `json:"title"`
	FeedURL   string `json:"feed_url"`
	SiteURL   string `json:"site_url,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Score     int    `json:"score"`
	Reason    string `json:"reason,omitempty"`
	IsDefault bool   `json:"is_default"`
}

func RankDiscoveryCandidates(candidates []FeedDiscoveryCandidate) []FeedDiscoveryCandidate {
	sorted := append([]FeedDiscoveryCandidate(nil), candidates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Score == sorted[j].Score {
			return sorted[i].FeedURL < sorted[j].FeedURL
		}
		return sorted[i].Score > sorted[j].Score
	})
	for i := range sorted {
		sorted[i].IsDefault = i == 0
	}
	return sorted
}

func ExtractFeedCandidatesFromHTML(pageURL string, html string) []FeedDiscoveryCandidate {
	matches := feedAlternateLinkPattern.FindAllString(html, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(matches))
	candidates := make([]FeedDiscoveryCandidate, 0, len(matches))
	for _, linkTag := range matches {
		rel := extractAttributeValue(feedRelAttrPattern, linkTag)
		if !containsAlternateRel(rel) {
			continue
		}

		contentType := strings.ToLower(strings.TrimSpace(extractAttributeValue(feedTypeAttrPattern, linkTag)))
		if contentType != "application/rss+xml" && contentType != "application/atom+xml" {
			continue
		}

		href := strings.TrimSpace(extractAttributeValue(feedHrefAttrPattern, linkTag))
		resolved := resolveCandidateURL(pageURL, href)
		if resolved == "" {
			continue
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}

		title := strings.TrimSpace(extractAttributeValue(feedTitleAttrPattern, linkTag))
		if title == "" {
			title = "Detected Feed"
		}

		kind := "alternate"
		score := 10
		reason := "detected from HTML alternate link"
		lowerTitle := strings.ToLower(title)
		lowerURL := strings.ToLower(resolved)
		if strings.Contains(lowerTitle, "comment") {
			kind = "comments"
			score = 20
			reason = "comment feed discovered from alternate link"
		} else if strings.Contains(lowerTitle, "main") || strings.Contains(lowerTitle, "rss") || strings.Contains(lowerTitle, "feed") || strings.Contains(lowerURL, "/feed") || strings.Contains(lowerURL, "/rss") {
			kind = "main"
			score = 40
			reason = "main feed discovered from alternate link"
		}

		candidates = append(candidates, FeedDiscoveryCandidate{
			Title:   title,
			FeedURL: resolved,
			SiteURL: pageURL,
			Kind:    kind,
			Score:   score,
			Reason:  reason,
		})
	}

	return RankDiscoveryCandidates(candidates)
}

func resolveCandidateURL(pageURL, raw string) string {
	trimmedRaw := strings.TrimSpace(raw)
	if trimmedRaw == "" {
		return ""
	}

	base, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil || base == nil || !base.IsAbs() {
		return ""
	}

	href, err := url.Parse(trimmedRaw)
	if err != nil || href == nil {
		return ""
	}

	resolved := base.ResolveReference(href)
	if resolved == nil || !resolved.IsAbs() {
		return ""
	}
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return ""
	}
	return resolved.String()
}

func extractAttributeValue(pattern *regexp.Regexp, input string) string {
	match := pattern.FindStringSubmatch(input)
	if len(match) < 2 {
		return ""
	}
	for _, value := range match[1:] {
		if value != "" {
			return htmlEntityMinimalUnescape(value)
		}
	}
	return ""
}

func containsAlternateRel(rel string) bool {
	for _, part := range strings.Fields(strings.ToLower(rel)) {
		if part == "alternate" {
			return true
		}
	}
	return false
}

func htmlEntityMinimalUnescape(input string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
	)
	return replacer.Replace(input)
}
