package feed

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
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

func TestDiscoverFeedCandidatesFetchesWebsiteOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
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

	router := gin.New()
	router.POST("/discover", DiscoverFeedCandidates())
	req := httptest.NewRequest(http.MethodPost, "/discover", strings.NewReader(`{"url":"https://example.com/blog"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	if requestCount != 1 {
		t.Fatalf("expected website discovery to make one request, got %d", requestCount)
	}
}

func TestResolveSubscriptionInputBlocksPrivateURLBeforeFetch(t *testing.T) {
	originalClient := feedDiscoveryHTTPClient
	originalResolver := resolveFeedDiscoveryHostname
	requestCount := 0
	feedDiscoveryHTTPClient = &http.Client{Transport: feedDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(`<html></html>`)),
			Request:    req,
		}, nil
	})}
	resolveFeedDiscoveryHostname = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	t.Cleanup(func() {
		feedDiscoveryHTTPClient = originalClient
		resolveFeedDiscoveryHostname = originalResolver
	})

	response, statusCode := resolveSubscriptionInputForUser(nil, uuid.Nil, "https://example.com/feed")
	if statusCode != http.StatusOK || response.Status != "invalid" {
		t.Fatalf("expected invalid response, got status=%d response=%#v", statusCode, response)
	}
	if requestCount != 0 {
		t.Fatalf("expected blocked URL to avoid fetches, got %d", requestCount)
	}
}

func TestFeedDiscoveryHTTPClientUsesFourSecondTimeout(t *testing.T) {
	if feedDiscoveryHTTPClient.Timeout != 4*time.Second {
		t.Fatalf("expected 4 second timeout, got %s", feedDiscoveryHTTPClient.Timeout)
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
