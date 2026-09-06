package feed

import (
	"net/url"
	"strings"

	"atoman/internal/model"
	"atoman/internal/service"
)

func isGithubHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "github.com" || host == "www.github.com"
}

func platformSubscriptionTargetURL(raw string) (autoSubscriptionTarget, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || !u.IsAbs() {
		return autoSubscriptionTarget{}, false
	}
	return platformSubscriptionTarget(u)
}

func platformSubscriptionTarget(u *url.URL) (autoSubscriptionTarget, bool) {
	if u == nil {
		return autoSubscriptionTarget{}, false
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	parts := splitURLPath(u)
	switch host {
	case "youtube.com", "m.youtube.com":
		if len(parts) >= 2 && parts[0] == "channel" && validPlatformSegment(parts[1]) {
			return buildPlatformTarget("youtube", "video", "youtube/channel", parts[1], u.String(), "YouTube "+parts[1])
		}
		if len(parts) >= 2 && parts[0] == "user" && validPlatformSegment(parts[1]) {
			return buildPlatformTarget("youtube", "video", "youtube/user", parts[1], u.String(), "YouTube "+parts[1])
		}
		if (len(parts) == 1 || (len(parts) == 2 && parts[1] == "videos")) && strings.HasPrefix(parts[0], "@") && validPlatformSegment(parts[0]) {
			return buildPlatformTarget("youtube", "video", "youtube/user", parts[0], u.String(), "YouTube "+parts[0])
		}
		if len(parts) == 1 && parts[0] == "playlist" {
			id := strings.TrimSpace(u.Query().Get("list"))
			if validPlatformSegment(id) {
				return buildPlatformTarget("youtube", "video", "youtube/playlist", id, u.String(), "YouTube 播放列表")
			}
		}
	case "space.bilibili.com":
		if len(parts) == 1 && validPlatformSegment(parts[0]) {
			return buildPlatformTarget("bilibili", "video", "bilibili/user/video", parts[0], u.String(), "Bilibili "+parts[0])
		}
	case "bilibili.com":
		if len(parts) == 2 && parts[0] == "space" && validPlatformSegment(parts[1]) {
			return buildPlatformTarget("bilibili", "video", "bilibili/user/video", parts[1], u.String(), "Bilibili "+parts[1])
		}
	}
	return autoSubscriptionTarget{}, false
}

func splitURLPath(u *url.URL) []string {
	raw := strings.Trim(u.EscapedPath(), "/")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, "/")
	for index := range parts {
		decoded, err := url.PathUnescape(parts[index])
		if err != nil {
			return nil
		}
		parts[index] = strings.TrimSpace(decoded)
	}
	return parts
}

func validPlatformSegment(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "/\\?#") {
		return false
	}
	for _, r := range value {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

func buildPlatformTarget(platform, contentType, template, id, siteURL, title string) (autoSubscriptionTarget, bool) {
	feedURL, err := service.BuildRSSHubFeedURL(template, map[string]string{"id": id, "username": id, "list": id, "uid": id})
	if err != nil {
		return autoSubscriptionTarget{}, false
	}
	return autoSubscriptionTarget{
		Provider:    "rsshub",
		Platform:    platform,
		ContentType: contentType,
		SourceType:  "external_rss",
		Title:       title,
		RssURL:      feedURL,
		SiteURL:     siteURL,
		Canonical:   normalizeCanonicalFeedURL(feedURL),
		Category:    contentType,
	}, true
}

func platformMetadataForSource(source model.FeedSource) (string, string) {
	if platform := platformForRSSURL(source.RssURL); platform != "" {
		return platform, contentTypeForPlatform(platform)
	}
	value := strings.ToLower(strings.TrimSpace(source.SiteURL + " " + source.CanonicalURL + " " + source.RssURL))
	if strings.Contains(value, "github") {
		return "github", "project_update"
	}
	if strings.Contains(value, "youtube") {
		return "youtube", "video"
	}
	if strings.Contains(value, "bilibili") {
		return "bilibili", "video"
	}
	return "", ""
}
