package service

import (
	"fmt"
	"strings"
	"time"

	"atoman/internal/model"

	"gorm.io/gorm"
)

type FeedReaderBackfillOptions struct {
	Limit          int
	PublishedAfter time.Time
	Apply          bool
	Requeue        bool
}

type FeedReaderBackfillResult struct {
	Scanned  int `json:"scanned"`
	Updated  int `json:"updated"`
	Requeued int `json:"requeued"`
	Skipped  int `json:"skipped"`
}

func feedReaderBackfillQuery(db *gorm.DB, options FeedReaderBackfillOptions) *gorm.DB {
	queryFilter := `feed_items.reader_version < ? AND (
		COALESCE(feed_items.feed_content_html, '') <> '' OR COALESCE(feed_items.full_text_html, '') <> ''`
	queryArgs := []any{ReaderVersionCurrent}
	if options.Requeue {
		queryFilter += ` OR (COALESCE(feed_items.reader_html, '') = '' AND feed_items.full_text_status IN ?)`
		queryArgs = append(queryArgs, []string{FullTextStatusDisabled, FullTextStatusFailed})
	}
	queryFilter += ")"
	query := db.Model(&model.FeedItem{}).
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_sources.deleted_at IS NULL").
		Where("feed_sources.source_type = ?", "external_rss").
		Where("feed_sources.rss_url NOT LIKE ?", "%/feed/rss/%").
		Where(queryFilter, queryArgs...)
	if !options.PublishedAfter.IsZero() {
		query = query.Where("feed_items.published_at >= ?", options.PublishedAfter)
	}
	return query
}

// BackfillFeedReaderContent rebuilds selected reader content from already stored
// feed/page candidates. It only requeues network work when explicitly requested.
func BackfillFeedReaderContent(db *gorm.DB, options FeedReaderBackfillOptions) (FeedReaderBackfillResult, error) {
	if options.Limit <= 0 || options.Limit > 10000 {
		options.Limit = 1000
	}

	query := feedReaderBackfillQuery(db, options).
		Preload("FeedSource").
		Order("feed_items.published_at DESC, feed_items.id DESC").
		Limit(options.Limit)
	var items []model.FeedItem
	if err := query.Find(&items).Error; err != nil {
		return FeedReaderBackfillResult{}, err
	}

	result := FeedReaderBackfillResult{Scanned: len(items)}
	for index := range items {
		item := &items[index]
		var feedCandidate ReaderCandidate
		if candidate, err := SanitizeFeedContent(item.Link, item.FeedContentHTML); err == nil {
			feedCandidate = candidate
		}
		var pageCandidate ReaderCandidate
		if strings.TrimSpace(item.FullTextHTML) != "" {
			if candidate, err := sanitizeReaderFragment(item.Link, item.FullTextHTML); err == nil {
				candidate.Source = ReaderSourcePage
				candidate.Extractor = "backfill"
				pageCandidate = candidate
			}
		}
		selected := ChooseReaderCandidate(feedCandidate, pageCandidate)
		if selected.HTML != "" {
			result.Updated++
			if !options.Apply {
				continue
			}
			updates := map[string]any{
				"reader_html":          selected.HTML,
				"reader_source":        selected.Source,
				"reader_quality_score": selected.QualityScore,
				"reader_quality_flags": ReaderQualityFlagsJSON(selected.QualityFlags),
				"reader_version":       ReaderVersionCurrent,
				"reader_content_hash":  selected.ContentHash,
			}
			if err := db.Model(&model.FeedItem{}).Where("id = ?", item.ID).Updates(updates).Error; err != nil {
				return result, fmt.Errorf("update reader item %s: %w", item.ID, err)
			}
			continue
		}

		canRequeue := options.Requeue && item.FeedSource != nil && IsFeedItemEligibleForFullText(*item.FeedSource, *item) &&
			(item.FullTextStatus == FullTextStatusDisabled || item.FullTextStatus == FullTextStatusFailed)
		if !canRequeue {
			result.Skipped++
			if options.Apply && options.Requeue {
				if err := db.Model(&model.FeedItem{}).Where("id = ?", item.ID).Update("reader_version", ReaderVersionCurrent).Error; err != nil {
					return result, fmt.Errorf("mark reader item %s evaluated: %w", item.ID, err)
				}
			}
			continue
		}
		result.Requeued++
		if options.Apply {
			if err := db.Model(&model.FeedItem{}).Where("id = ?", item.ID).Updates(map[string]any{
				"full_text_status":          FullTextStatusPending,
				"full_text_attempt_count":   0,
				"full_text_error_code":      "",
				"full_text_error":           "",
				"next_full_text_attempt_at": nil,
				"reader_version":            ReaderVersionCurrent,
			}).Error; err != nil {
				return result, fmt.Errorf("requeue reader item %s: %w", item.ID, err)
			}
		}
	}
	return result, nil
}
