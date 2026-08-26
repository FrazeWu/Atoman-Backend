package service

import (
	"net/url"
	"strings"
)

// NormalizeFeedSourceURL produces one stable identity for equivalent HTTP feed URLs.
// Query parameters are preserved because they can select different feeds.
func NormalizeFeedSourceURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return strings.TrimRight(trimmed, "/")
	}

	normalized := *parsed
	normalized.Scheme = "https"
	normalized.Host = strings.ToLower(normalized.Host)
	normalized.Host = strings.TrimPrefix(normalized.Host, "www.")
	normalized.Path = strings.TrimRight(normalized.Path, "/")
	normalized.Fragment = ""
	normalized.RawFragment = ""
	return normalized.String()
}
