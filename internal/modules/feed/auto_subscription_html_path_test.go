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

func TestFetchAutoSubscriptionDocumentFetchesWebsiteOnce(t *testing.T) {
	originalClient := feedDiscoveryHTTPClient
	originalResolver := resolveFeedDiscoveryHostname
	requestCount := 0
	feedDiscoveryHTTPClient = &http.Client{Transport: feedDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(`<html><head><link rel="alternate" type="application/rss+xml" href="/feed.xml"></head></html>`)),
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

	document, err := fetchAutoSubscriptionDocument("https://example.com/blog")
	if err != nil {
		t.Fatalf("fetch document: %v", err)
	}
	if document.IsFeed {
		t.Fatal("expected HTML document, got feed")
	}
	if !strings.Contains(document.Body, `href="/feed.xml"`) {
		t.Fatalf("expected response body to be retained, got %q", document.Body)
	}
	if requestCount != 1 {
		t.Fatalf("expected one fetch, got %d", requestCount)
	}
}

func TestFetchAutoSubscriptionDocumentRecognizesDirectFeed(t *testing.T) {
	originalClient := feedDiscoveryHTTPClient
	originalResolver := resolveFeedDiscoveryHostname
	feedDiscoveryHTTPClient = &http.Client{Transport: feedDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/rss+xml"}},
			Body:       io.NopCloser(strings.NewReader(`<?xml version="1.0"?><rss version="2.0"></rss>`)),
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

	document, err := fetchAutoSubscriptionDocument("https://example.com/feed")
	if err != nil {
		t.Fatalf("fetch document: %v", err)
	}
	if !document.IsFeed {
		t.Fatal("expected RSS document to be recognized")
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
