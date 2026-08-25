package feed

import (
	"encoding/json"
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
