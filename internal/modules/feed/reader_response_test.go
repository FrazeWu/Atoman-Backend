package feed

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/service"
)

func TestNewFeedItemReaderResponseUsesCanonicalVariants(t *testing.T) {
	item := model.FeedItem{
		ReaderSource:      service.ReaderSourcePage,
		FeedContentHTML:   "<p>RSS content</p>",
		FullTextHTML:      "<p>web content</p>",
		FullTextStatus:    service.FullTextStatusSuccess,
		FullTextWordCount: 1200,
	}

	reader := newFeedItemReaderResponse(item)
	if reader.DefaultVariant != FeedReaderVariantFullText {
		t.Fatalf("default_variant=%q", reader.DefaultVariant)
	}
	if reader.RSS == nil || reader.RSS.HTML != item.FeedContentHTML {
		t.Fatalf("rss=%+v", reader.RSS)
	}
	if reader.FullText.HTML != item.FullTextHTML || reader.FullText.WordCount != item.FullTextWordCount {
		t.Fatalf("full_text=%+v", reader.FullText)
	}
}

func TestNewFeedItemReaderResponseHidesStaleFullText(t *testing.T) {
	item := model.FeedItem{
		ReaderSource:    service.ReaderSourcePage,
		FeedContentHTML: "<p>RSS content</p>",
		FullTextHTML:    "<p>stale web content</p>",
		FullTextStatus:  service.FullTextStatusRetry,
	}

	reader := newFeedItemReaderResponse(item)
	if reader.DefaultVariant != FeedReaderVariantRSS {
		t.Fatalf("default_variant=%q", reader.DefaultVariant)
	}
	if reader.FullText.HTML != "" || reader.FullText.Status != service.FullTextStatusRetry {
		t.Fatalf("full_text=%+v", reader.FullText)
	}
}

func TestFeedItemDetailResponseProxiesMediaAndPrefersContentImage(t *testing.T) {
	t.Setenv("FEED_IMAGE_PROXY_PUBLIC_URL", "/api/v1/feed/media/image")
	t.Setenv("FEED_IMAGE_PROXY_SECRET", "image-proxy-secret-for-tests-32bytes")

	sourceCoverURL := "https://cdn.example.com/source-cover.jpg"
	contentImageURL := "https://cdn.example.com/article-cover.jpg"
	source := &model.FeedSource{CoverURL: sourceCoverURL}
	item := model.FeedItem{
		ImageURL:        sourceCoverURL,
		FeedSource:      source,
		Link:            "https://example.com/articles/1",
		FeedContentHTML: `<p>正文</p><img src="https://cdn.example.com/article-cover.jpg">`,
	}

	response := newFeedItemDetailResponse(item)
	if response.Item == nil {
		t.Fatal("response item is nil")
	}
	itemImage, err := url.Parse(response.Item.ImageURL)
	if err != nil {
		t.Fatal(err)
	}
	if itemImage.Query().Get("url") != contentImageURL || itemImage.Query().Get("sig") == "" {
		t.Fatalf("item image=%q", response.Item.ImageURL)
	}
	sourceImage, err := url.Parse(response.Item.FeedSource.CoverURL)
	if err != nil {
		t.Fatal(err)
	}
	if sourceImage.Query().Get("url") != sourceCoverURL || sourceImage.Query().Get("sig") == "" {
		t.Fatalf("source image=%q", response.Item.FeedSource.CoverURL)
	}
	if item.ImageURL != sourceCoverURL || item.FeedSource.CoverURL != sourceCoverURL {
		t.Fatalf("input item mutated: %+v", item)
	}
}

func TestFeedItemDetailResponseKeepsBodyOutOfMetadata(t *testing.T) {
	item := model.FeedItem{
		ReaderHTML:      "<p>selected reader content</p>",
		FeedContentHTML: "<p>RSS content</p>",
		FullTextHTML:    "<p>web content</p>",
		FullTextStatus:  service.FullTextStatusSuccess,
	}
	payload := FeedItemDetailResponse{
		Item:   &item,
		Reader: newFeedItemReaderResponse(item),
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, field := range []string{"reader_html", "content_html", "feed_content_html", "full_text_html"} {
		if strings.Contains(body, `"`+field+`"`) {
			t.Fatalf("metadata leaked %s: %s", field, body)
		}
	}
	if !strings.Contains(body, `"reader"`) || !strings.Contains(body, `"full_text"`) {
		t.Fatalf("reader variants missing: %s", body)
	}
}
