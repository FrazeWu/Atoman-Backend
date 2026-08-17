package service

import (
	"strings"
	"testing"
)

func TestExtractFeedImageMetadataUsesPageMetadataBeforeContentAndIcon(t *testing.T) {
	metadata, err := ExtractFeedImageMetadata("https://example.com/articles/one", strings.NewReader(`
		<html><head>
			<meta name="twitter:image" content="/twitter.jpg">
			<meta property="og:image" content="/og.jpg">
			<link rel="icon" href="/favicon.ico">
		</head><body>
			<article><img src="/body.jpg" alt="article image"></article>
		</body></html>`))
	if err != nil {
		t.Fatalf("extract metadata: %v", err)
	}
	if metadata.ImageURL != "https://example.com/og.jpg" {
		t.Fatalf("image=%q", metadata.ImageURL)
	}
	if metadata.IconURL != "https://example.com/favicon.ico" {
		t.Fatalf("icon=%q", metadata.IconURL)
	}
}

func TestExtractFeedImageMetadataFallsBackToJSONLDContentAndIcon(t *testing.T) {
	jsonMetadata, err := ExtractFeedImageMetadata("https://example.com/articles/one", strings.NewReader(`
		<html><head><script type="application/ld+json">{"@type":"NewsArticle","image":{"url":"/structured.jpg"}}</script></head></html>`))
	if err != nil {
		t.Fatalf("extract JSON-LD metadata: %v", err)
	}
	if jsonMetadata.ImageURL != "https://example.com/structured.jpg" {
		t.Fatalf("JSON-LD image=%q", jsonMetadata.ImageURL)
	}

	contentMetadata, err := ExtractFeedImageMetadata("https://example.com/articles/one", strings.NewReader(`
		<html><body><article><img data-src="/lazy.jpg" alt="Article hero"></article></body></html>`))
	if err != nil {
		t.Fatalf("extract content metadata: %v", err)
	}
	if contentMetadata.ImageURL != "https://example.com/lazy.jpg" {
		t.Fatalf("content image=%q", contentMetadata.ImageURL)
	}

	iconMetadata, err := ExtractFeedImageMetadata("https://example.com/articles/one", strings.NewReader(`
		<html><head><link rel="apple-touch-icon" href="/touch.png"></head></html>`))
	if err != nil {
		t.Fatalf("extract icon metadata: %v", err)
	}
	if iconMetadata.ImageURL != "" || iconMetadata.IconURL != "https://example.com/touch.png" {
		t.Fatalf("icon fallback=%#v", iconMetadata)
	}
}

func TestResolveFeedImageURLRejectsUnsupportedSchemes(t *testing.T) {
	metadata, err := ExtractFeedImageMetadata("https://example.com/articles/one", strings.NewReader(`
		<html><head><meta property="og:image" content="data:image/png;base64,abc"></head></html>`))
	if err != nil {
		t.Fatalf("extract unsafe metadata: %v", err)
	}
	if metadata.ImageURL != "" {
		t.Fatalf("expected data URL to be ignored, got %q", metadata.ImageURL)
	}
}
