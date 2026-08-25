package feed

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestResolveSubscriptionInputRejectsHTMLFeedPath(t *testing.T) {
	stubFeedDiscoveryHTML(t, `<html><head><title>订阅流 | Atoman</title></head><body></body></html>`)

	response, statusCode := resolveSubscriptionInputForUser(
		nil,
		uuid.Nil,
		"https://www.atoman.org/feed?mode=hot&category=all&theme=all&language=zh",
	)

	if statusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, statusCode)
	}
	if response.Status != "not_found" {
		t.Fatalf("expected status not_found, got %q", response.Status)
	}
}

func TestAutoAddSubscriptionTargetRejectsHTMLFeedPath(t *testing.T) {
	stubFeedDiscoveryHTML(t, `<html><head><title>订阅流 | Atoman</title></head><body></body></html>`)

	_, err := autoSubscriptionTargetForAdd(nil, uuid.Nil, AutoSubscriptionAddRequest{
		Input: "https://www.atoman.org/feed?mode=hot&category=all&theme=all&language=zh",
	})
	if err == nil {
		t.Fatal("expected HTML page at a feed-like path to be rejected")
	}
}

func stubFeedDiscoveryHTML(t *testing.T, html string) {
	t.Helper()

	originalClient := feedDiscoveryHTTPClient
	originalResolver := resolveFeedDiscoveryHostname
	feedDiscoveryHTTPClient = &http.Client{Transport: feedDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(html)),
			Request:    req,
		}, nil
	})}
	resolveFeedDiscoveryHostname = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	t.Cleanup(func() {
		feedDiscoveryHTTPClient = originalClient
		resolveFeedDiscoveryHostname = originalResolver
	})
}
