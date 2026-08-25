package service

import (
	"fmt"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const fullTextSourceLeaseDuration = 15 * time.Minute

func claimFullTextBatch(db *gorm.DB, now time.Time, limit int) ([]claimedFullTextItem, error) {
	if limit < 1 {
		return nil, nil
	}

	claimed := make([]claimedFullTextItem, 0, limit)
	err := db.Transaction(func(tx *gorm.DB) error {
		candidates, err := listFullTextClaimCandidates(tx, now, limit)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}

		expiresAt := now.Add(fullTextSourceLeaseDuration)
		sourceIDs := make([]uuid.UUID, 0, len(candidates))
		for _, candidate := range candidates {
			leaseToken := uuid.NewString()
			lease := tx.Model(&model.FeedSource{}).
				Where("id = ? AND full_text_enabled = ?", candidate.FeedSourceID, true).
				Where("full_text_lease_until IS NULL OR full_text_lease_until <= ?", now).
				Updates(map[string]any{
					"full_text_lease_token": leaseToken,
					"full_text_lease_until": expiresAt,
				})
			if lease.Error != nil {
				return lease.Error
			}
			if lease.RowsAffected == 0 {
				continue
			}

			attemptCount := candidate.FullTextAttemptCount + 1
			itemUpdate := tx.Model(&model.FeedItem{}).
				Where("id = ? AND full_text_status IN ?", candidate.ID, []string{FullTextStatusPending, FullTextStatusRetry}).
				Where("next_full_text_attempt_at IS NULL OR next_full_text_attempt_at <= ?", now).
				Updates(map[string]any{
					"full_text_status":          FullTextStatusFetching,
					"full_text_attempt_count":   attemptCount,
					"last_full_text_attempt_at": &now,
					"next_full_text_attempt_at": nil,
				})
			if itemUpdate.Error != nil {
				return itemUpdate.Error
			}
			if itemUpdate.RowsAffected == 0 {
				if err := releaseFullTextSourceLease(tx, candidate.FeedSourceID, leaseToken); err != nil {
					return err
				}
				continue
			}

			candidate.FullTextStatus = FullTextStatusFetching
			candidate.FullTextAttemptCount = attemptCount
			candidate.LastFullTextAttemptAt = &now
			candidate.NextFullTextAttemptAt = nil
			claimed = append(claimed, claimedFullTextItem{item: candidate, leaseToken: leaseToken})
			sourceIDs = append(sourceIDs, candidate.FeedSourceID)
		}
		if len(claimed) == 0 {
			return nil
		}

		var sources []model.FeedSource
		if err := tx.Where("id IN ?", sourceIDs).Find(&sources).Error; err != nil {
			return err
		}
		sourcesByID := make(map[uuid.UUID]model.FeedSource, len(sources))
		for _, source := range sources {
			sourcesByID[source.ID] = source
		}
		for index := range claimed {
			source, ok := sourcesByID[claimed[index].item.FeedSourceID]
			if !ok {
				return fmt.Errorf("full text source %s was not found after claim", claimed[index].item.FeedSourceID)
			}
			claimed[index].source = source
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func listFullTextClaimCandidates(db *gorm.DB, now time.Time, limit int) ([]model.FeedItem, error) {
	var candidates []model.FeedItem
	err := db.Raw(`
WITH source_candidates AS (
	SELECT DISTINCT ON (feed_items.feed_source_id)
		feed_items.id,
		CASE
			WHEN EXISTS (SELECT 1 FROM reading_list_items WHERE target_type = 'feed_item' AND target_id = feed_items.id) THEN 0
			WHEN EXISTS (SELECT 1 FROM feed_item_stars WHERE feed_item_id = feed_items.id) THEN 1
			ELSE 2
		END AS priority,
		feed_items.created_at,
		feed_items.published_at
	FROM feed_items
	JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id
	WHERE feed_items.deleted_at IS NULL
		AND feed_sources.deleted_at IS NULL
		AND feed_items.full_text_status IN (?, ?)
		AND (feed_items.next_full_text_attempt_at IS NULL OR feed_items.next_full_text_attempt_at <= ?)
		AND feed_sources.source_type = 'external_rss'
		AND feed_sources.full_text_enabled = TRUE
		AND (feed_sources.full_text_lease_until IS NULL OR feed_sources.full_text_lease_until <= ?)
		AND feed_sources.rss_url NOT LIKE '%/feed/rss/%'
		AND COALESCE(feed_items.enclosure_url, '') = ''
		AND COALESCE(feed_items.enclosure_type, '') NOT LIKE 'audio/%'
		AND COALESCE(feed_items.enclosure_type, '') NOT LIKE 'video/%'
		AND COALESCE(feed_items.duration, '') = ''
	ORDER BY feed_items.feed_source_id, priority, feed_items.created_at ASC, feed_items.published_at ASC
), prioritized AS (
	SELECT id, priority, created_at, published_at
	FROM source_candidates
	ORDER BY priority, created_at ASC, published_at ASC
	LIMIT ?
)
SELECT feed_items.*
FROM feed_items
JOIN prioritized ON prioritized.id = feed_items.id
ORDER BY prioritized.priority, prioritized.created_at ASC, prioritized.published_at ASC
FOR UPDATE OF feed_items SKIP LOCKED`, FullTextStatusPending, FullTextStatusRetry, now, now, limit).Scan(&candidates).Error
	return candidates, err
}

func releaseFullTextSourceLease(db *gorm.DB, sourceID uuid.UUID, leaseToken string) error {
	if leaseToken == "" {
		return nil
	}
	return db.Model(&model.FeedSource{}).
		Where("id = ? AND full_text_lease_token = ?", sourceID, leaseToken).
		Updates(map[string]any{"full_text_lease_token": "", "full_text_lease_until": nil}).Error
}
