package service

import (
	"time"

	"atoman/internal/model"
)

// FeedItemMatchesDuplicateRepresentative checks a later article against the
// original group's first item. It uses exactly the same comparison as the
// greedy full-scan algorithm; it does not compare against arbitrary members.
func FeedItemMatchesDuplicateRepresentative(normalizedURL, normalizedTitle string, representativePublishedAt time.Time, later model.FeedItem) bool {
	group := duplicateGroup{
		normalizedURL:   normalizedURL,
		normalizedTitle: normalizedTitle,
		publishedAt:     representativePublishedAt,
	}
	return group.matches(NormalizeFeedItemURL(later.Link), NormalizeFeedItemTitle(later.Title), later.PublishedAt)
}

// FeedItemDuplicateSourceTitle exposes the exact source-label fallback used in
// canonical duplicate groups, needed to transition singleton groups to pairs.
func FeedItemDuplicateSourceTitle(item model.FeedItem) string {
	return duplicateSourceTitle(item)
}
