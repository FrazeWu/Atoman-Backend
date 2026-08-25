package service

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"

	"atoman/internal/model"

	"gorm.io/gorm"
)

func fullTextURLHash(rawURL string) string {
	canonical := canonicalFullTextURL(rawURL)
	if canonical == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func canonicalFullTextURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return ""
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port != "" && !((parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80")) {
		host += ":" + port
	}
	parsed.Host = host
	parsed.User = nil
	parsed.Fragment = ""
	if parsed.Path == "" {
		parsed.Path = "/"
	}

	query := parsed.Query()
	for key := range query {
		if isFullTextTrackingQueryKey(key) {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.ForceQuery = false
	return parsed.String()
}

func isFullTextTrackingQueryKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.HasPrefix(key, "utm_") || key == "fbclid" || key == "gclid" || key == "mc_cid" || key == "mc_eid"
}

func findReusableFullText(db *gorm.DB, item model.FeedItem) (FullTextResult, bool, error) {
	urlHash := fullTextURLHash(item.Link)
	if urlHash == "" {
		return FullTextResult{}, false, nil
	}

	var reusable model.FeedItem
	query := db.Where("id <> ?", item.ID).
		Where("full_text_status = ?", FullTextStatusSuccess).
		Where("COALESCE(full_text_html, '') <> ''").
		Where("full_text_url_hash = ? OR (COALESCE(full_text_url_hash, '') = '' AND link = ?)", urlHash, item.Link).
		Order("full_text_fetched_at DESC NULLS LAST, created_at DESC")
	result := query.First(&reusable)
	if result.Error == nil {
		return FullTextResult{
			HTML:      reusable.FullTextHTML,
			WordCount: reusable.FullTextWordCount,
			Extractor: "reused",
		}, true, nil
	}
	if result.Error == gorm.ErrRecordNotFound {
		return FullTextResult{}, false, nil
	}
	return FullTextResult{}, false, result.Error
}
