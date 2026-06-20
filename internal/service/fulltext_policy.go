package service

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"atoman/internal/model"
)

var resolveFullTextHostname = net.LookupIP

func CalculateNextFullTextRetryAt(now time.Time, attempt int) (time.Time, bool) {
	switch attempt {
	case 1:
		return now.Add(time.Hour), false
	case 2:
		return now.Add(6 * time.Hour), false
	case 3:
		return now.Add(24 * time.Hour), false
	case 4:
		return now.Add(72 * time.Hour), false
	default:
		return time.Time{}, true
	}
}

func DefaultFullTextEnabled(sourceType string) bool {
	return sourceType == "external_rss"
}

func IsFeedItemEligibleForFullText(source model.FeedSource, item model.FeedItem) bool {
	if source.SourceType != "external_rss" || !source.FullTextEnabled {
		return false
	}
	if isPodcastLikeFeedItem(item) {
		return false
	}
	return ValidateFullTextTargetURL(item.Link) == nil
}

func defaultFullTextStatusForSource(source model.FeedSource, item model.FeedItem, looksLikeFullText bool) string {
	if !IsFeedItemEligibleForFullText(source, item) {
		return FullTextStatusDisabled
	}
	if looksLikeFullText {
		return FullTextStatusDisabled
	}
	return FullTextStatusPending
}

func isPodcastLikeFeedItem(item model.FeedItem) bool {
	enclosureType := strings.ToLower(strings.TrimSpace(item.EnclosureType))
	if strings.HasPrefix(enclosureType, "audio/") || strings.HasPrefix(enclosureType, "video/") {
		return true
	}
	enclosureURL := strings.ToLower(strings.TrimSpace(item.EnclosureURL))
	if enclosureURL != "" {
		for _, suffix := range []string{".mp3", ".m4a", ".aac", ".ogg", ".opus", ".wav", ".mp4", ".m4v", ".mov", ".webm"} {
			if strings.Contains(enclosureURL, suffix) {
				return true
			}
		}
	}
	return strings.TrimSpace(item.Duration) != ""
}

func ValidateFullTextTargetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() {
		return errors.New(FullTextErrorInvalidURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New(FullTextErrorInvalidURL)
	}

	hostname := strings.TrimSpace(u.Hostname())
	if hostname == "" || strings.EqualFold(hostname, "localhost") {
		return errors.New(FullTextErrorSSRFBlocked)
	}

	if addr, err := netip.ParseAddr(hostname); err == nil {
		if isBlockedFullTextIP(addr) {
			return errors.New(FullTextErrorSSRFBlocked)
		}
		return nil
	}

	ips, err := resolveFullTextHostname(hostname)
	if err != nil {
		return errors.New(FullTextErrorSSRFBlocked)
	}

	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		if isBlockedFullTextIP(addr) {
			return errors.New(FullTextErrorSSRFBlocked)
		}
	}

	return nil
}

func isBlockedFullTextIP(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsUnspecified() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsMulticast()
}
