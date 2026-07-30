package feed

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"atoman/internal/service"

	"github.com/gin-gonic/gin"
)

const feedDiscoveryMaxHTMLBytes = 2 * 1024 * 1024

var resolveFeedDiscoveryHostname = net.LookupIP

var feedDiscoveryHTTPClient = &http.Client{
	Timeout: 8 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return validateFeedDiscoveryFetchURL(req.URL)
	},
}

func DiscoverFeedCandidates() gin.HandlerFunc {
	return func(c *gin.Context) {
		var input struct {
			URL string `json:"url"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid discover request body"})
			return
		}

		rawURL := strings.TrimSpace(input.URL)
		u, err := url.ParseRequestURI(rawURL)
		if err != nil || u == nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "url must be an absolute http/https URL"})
			return
		}

		if isLikelyDirectFeedURL(u) {
			c.JSON(http.StatusOK, gin.H{"candidates": []service.FeedDiscoveryCandidate{
				{
					Title:     rawURL,
					FeedURL:   rawURL,
					SiteURL:   rawURL,
					Kind:      "direct",
					Score:     100,
					Reason:    "direct feed URL",
					IsDefault: true,
				},
			}})
			return
		}

		if err := validateFeedDiscoveryFetchURL(u); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "url is not allowed for feed discovery"})
			return
		}

		html, err := fetchFeedDiscoveryHTML(rawURL)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to fetch website for feed discovery"})
			return
		}

		candidates := service.ExtractFeedCandidatesFromHTML(rawURL, html)
		if candidates == nil {
			candidates = []service.FeedDiscoveryCandidate{}
		}
		c.JSON(http.StatusOK, gin.H{"candidates": candidates})
	}
}

func isLikelyDirectFeedURL(u *url.URL) bool {
	path := strings.ToLower(strings.TrimSpace(u.Path))
	if path == "" {
		return false
	}
	return strings.HasSuffix(path, ".xml") ||
		strings.HasSuffix(path, ".rss") ||
		strings.HasSuffix(path, ".atom") ||
		strings.HasSuffix(path, "/rss") ||
		strings.HasSuffix(path, "/atom") ||
		strings.HasSuffix(path, "/feed")
}

func fetchFeedDiscoveryHTML(targetURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("User-Agent", "AtomanFeedDiscoveryBot/1.0")

	resp, err := feedDiscoveryHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if contentType != "" && !strings.Contains(contentType, "text/html") && !strings.Contains(contentType, "application/xhtml+xml") {
		return "", fmt.Errorf("unsupported content type %q", contentType)
	}

	limited := io.LimitReader(resp.Body, feedDiscoveryMaxHTMLBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	if len(data) > feedDiscoveryMaxHTMLBytes {
		return "", errors.New("feed discovery html response too large")
	}
	return string(data), nil
}

func validateFeedDiscoveryFetchURL(u *url.URL) error {
	if u == nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("invalid feed discovery url")
	}

	hostname := strings.TrimSpace(u.Hostname())
	if hostname == "" || strings.EqualFold(hostname, "localhost") {
		return errors.New("blocked feed discovery url")
	}

	if addr, err := netip.ParseAddr(hostname); err == nil {
		if isBlockedFeedDiscoveryIP(addr) {
			return errors.New("blocked feed discovery url")
		}
		return nil
	}

	ips, err := resolveFeedDiscoveryHostname(hostname)
	if err != nil {
		return errors.New("blocked feed discovery url")
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		if isBlockedFeedDiscoveryIP(addr) {
			return errors.New("blocked feed discovery url")
		}
	}
	return nil
}

func isBlockedFeedDiscoveryIP(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsUnspecified() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsMulticast()
}
