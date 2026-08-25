package feed

import (
	"strings"

	"atoman/internal/model"
	"atoman/internal/service"
)

type FeedReaderVariant string

const (
	FeedReaderVariantRSS      FeedReaderVariant = "rss"
	FeedReaderVariantFullText FeedReaderVariant = "full_text"
	FeedReaderVariantSummary  FeedReaderVariant = "summary"
)

type FeedReaderContentResponse struct {
	HTML string `json:"html"`
}

type FeedReaderFullTextResponse struct {
	Status    string `json:"status"`
	HTML      string `json:"html,omitempty"`
	WordCount int    `json:"word_count,omitempty"`
}

type FeedItemReaderResponse struct {
	DefaultVariant FeedReaderVariant          `json:"default_variant"`
	RSS            *FeedReaderContentResponse `json:"rss"`
	FullText       FeedReaderFullTextResponse `json:"full_text"`
}

type FeedItemDetailResponse struct {
	Item   *model.FeedItem        `json:"item"`
	Reader FeedItemReaderResponse `json:"reader"`
}

func newFeedItemReaderResponse(item model.FeedItem) FeedItemReaderResponse {
	rssHTML := strings.TrimSpace(item.FeedContentHTML)
	fullTextHTML := ""
	if item.FullTextStatus == service.FullTextStatusSuccess {
		fullTextHTML = strings.TrimSpace(item.FullTextHTML)
	}

	response := FeedItemReaderResponse{
		DefaultVariant: defaultFeedReaderVariant(item.ReaderSource, rssHTML, fullTextHTML),
		FullText: FeedReaderFullTextResponse{
			Status: item.FullTextStatus,
		},
	}
	if rssHTML != "" {
		response.RSS = &FeedReaderContentResponse{HTML: rssHTML}
	}
	if fullTextHTML != "" {
		response.FullText.HTML = fullTextHTML
		response.FullText.WordCount = item.FullTextWordCount
	}
	return response
}

func defaultFeedReaderVariant(readerSource, rssHTML, fullTextHTML string) FeedReaderVariant {
	switch readerSource {
	case service.ReaderSourceFeed:
		if rssHTML != "" {
			return FeedReaderVariantRSS
		}
	case service.ReaderSourcePage:
		if fullTextHTML != "" {
			return FeedReaderVariantFullText
		}
	}
	if rssHTML != "" {
		return FeedReaderVariantRSS
	}
	if fullTextHTML != "" {
		return FeedReaderVariantFullText
	}
	return FeedReaderVariantSummary
}

func feedReaderVariantFromSource(readerSource string) FeedReaderVariant {
	switch readerSource {
	case service.ReaderSourceFeed:
		return FeedReaderVariantRSS
	case service.ReaderSourcePage:
		return FeedReaderVariantFullText
	default:
		return FeedReaderVariantSummary
	}
}
