package service

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFetchAndParseRSSParsesPodcastImages(t *testing.T) {
	originalClient := rssFetchHTTPClient
	rssFetchHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://feeds.example.com/podcast.xml" {
			return nil, errors.New("unexpected feed url: " + req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/rss+xml; charset=utf-8"}},
			Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"
  xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
  <channel>
    <title>不明白播客</title>
    <itunes:image href="https://cdn.example.com/show-cover.jpg"/>
    <item>
      <title>第 1 期</title>
      <link>https://example.com/ep-1</link>
      <guid>ep-1</guid>
      <description>summary</description>
      <pubDate>Mon, 02 Jun 2026 08:00:00 +0000</pubDate>
      <enclosure url="https://cdn.example.com/ep-1.mp3" type="audio/mpeg"/>
      <itunes:image href="https://cdn.example.com/ep-1-cover.jpg"/>
    </item>
  </channel>
</rss>`)),
		}, nil
	})}
	defer func() {
		rssFetchHTTPClient = originalClient
	}()

	items, title, coverURL, err := FetchAndParseRSS("https://feeds.example.com/podcast.xml")
	if err != nil {
		t.Fatalf("FetchAndParseRSS returned error: %v", err)
	}
	if title != "不明白播客" {
		t.Fatalf("expected title 不明白播客, got %q", title)
	}
	if coverURL != "https://cdn.example.com/show-cover.jpg" {
		t.Fatalf("expected show cover URL, got %q", coverURL)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].ITunesImage.Href != "https://cdn.example.com/ep-1-cover.jpg" {
		t.Fatalf("expected item cover URL, got %q", items[0].ITunesImage.Href)
	}
}
