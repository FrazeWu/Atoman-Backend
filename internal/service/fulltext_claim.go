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
			return fmt.Errorf("list full text claim candidates: %w", err)
		}
		if len(candidates) == 0 {
			return nil
		}

		sourceIDs := uniqueFullTextSourceIDs(candidates)
		leaseToken := uuid.NewString()
		lease := tx.Model(&model.FeedSource{}).
			Where("id IN ? AND full_text_enabled = ?", sourceIDs, true).
			Where("full_text_lease_until IS NULL OR full_text_lease_until <= ?", now).
			Updates(map[string]any{
				"full_text_lease_token": leaseToken,
				"full_text_lease_until": now.Add(fullTextSourceLeaseDuration),
			})
		if lease.Error != nil {
			return fmt.Errorf("lease full text sources: %w", lease.Error)
		}

		var leasedSources []model.FeedSource
		if err := tx.Select("id").
			Where("id IN ? AND full_text_lease_token = ?", sourceIDs, leaseToken).
			Find(&leasedSources).Error; err != nil {
			return fmt.Errorf("load leased full text sources: %w", err)
		}
		leasedSourceIDs := make([]uuid.UUID, 0, len(leasedSources))
		leasedByID := make(map[uuid.UUID]bool, len(leasedSources))
		for _, source := range leasedSources {
			leasedSourceIDs = append(leasedSourceIDs, source.ID)
			leasedByID[source.ID] = true
		}

		initialCandidates := filterFullTextCandidatesBySource(candidates, leasedByID)
		initialClaims, err := markFullTextItemsFetching(tx, initialCandidates, now)
		if err != nil {
			return err
		}
		claimed = appendClaimedFullTextItems(claimed, initialClaims, leaseToken)

		usedSourceIDs := uniqueFullTextSourceIDs(initialClaims)
		if len(claimed) < limit && len(usedSourceIDs) > 0 {
			additional, err := listAdditionalFullTextClaimCandidates(tx, now, usedSourceIDs, limit-len(claimed))
			if err != nil {
				return fmt.Errorf("list additional full text claim candidates: %w", err)
			}
			additionalClaims, err := markFullTextItemsFetching(tx, additional, now)
			if err != nil {
				return err
			}
			claimed = appendClaimedFullTextItems(claimed, additionalClaims, leaseToken)
		}
		if len(claimed) == 0 {
			return releaseFullTextSourceLeases(tx, leasedSourceIDs, leaseToken)
		}

		usedSourceIDs = uniqueFullTextSourceIDsFromClaims(claimed)
		unusedSourceIDs := subtractFullTextSourceIDs(leasedSourceIDs, usedSourceIDs)
		if err := releaseFullTextSourceLeases(tx, unusedSourceIDs, leaseToken); err != nil {
			return err
		}

		var sources []model.FeedSource
		if err := tx.Where("id IN ?", usedSourceIDs).Find(&sources).Error; err != nil {
			return fmt.Errorf("load claimed full text sources: %w", err)
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

func uniqueFullTextSourceIDs(items []model.FeedItem) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(items))
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		if seen[item.FeedSourceID] {
			continue
		}
		seen[item.FeedSourceID] = true
		ids = append(ids, item.FeedSourceID)
	}
	return ids
}

func uniqueFullTextSourceIDsFromClaims(claims []claimedFullTextItem) []uuid.UUID {
	items := make([]model.FeedItem, 0, len(claims))
	for _, claim := range claims {
		items = append(items, claim.item)
	}
	return uniqueFullTextSourceIDs(items)
}

func filterFullTextCandidatesBySource(items []model.FeedItem, sourceIDs map[uuid.UUID]bool) []model.FeedItem {
	filtered := make([]model.FeedItem, 0, len(items))
	for _, item := range items {
		if sourceIDs[item.FeedSourceID] {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func subtractFullTextSourceIDs(all []uuid.UUID, included []uuid.UUID) []uuid.UUID {
	includedSet := make(map[uuid.UUID]bool, len(included))
	for _, id := range included {
		includedSet[id] = true
	}
	remaining := make([]uuid.UUID, 0, len(all))
	for _, id := range all {
		if !includedSet[id] {
			remaining = append(remaining, id)
		}
	}
	return remaining
}

func appendClaimedFullTextItems(claimed []claimedFullTextItem, items []model.FeedItem, leaseToken string) []claimedFullTextItem {
	for _, item := range items {
		claimed = append(claimed, claimedFullTextItem{item: item, leaseToken: leaseToken})
	}
	return claimed
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

func listAdditionalFullTextClaimCandidates(db *gorm.DB, now time.Time, sourceIDs []uuid.UUID, limit int) ([]model.FeedItem, error) {
	if limit < 1 || len(sourceIDs) == 0 {
		return nil, nil
	}

	var candidates []model.FeedItem
	err := db.Raw(`
SELECT feed_items.*
FROM feed_items
JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id
WHERE feed_items.deleted_at IS NULL
	AND feed_items.feed_source_id IN ?
	AND feed_items.full_text_status IN (?, ?)
	AND (feed_items.next_full_text_attempt_at IS NULL OR feed_items.next_full_text_attempt_at <= ?)
	AND feed_sources.full_text_enabled = TRUE
	AND COALESCE(feed_items.enclosure_url, '') = ''
	AND COALESCE(feed_items.enclosure_type, '') NOT LIKE 'audio/%'
	AND COALESCE(feed_items.enclosure_type, '') NOT LIKE 'video/%'
	AND COALESCE(feed_items.duration, '') = ''
ORDER BY CASE
	WHEN EXISTS (SELECT 1 FROM reading_list_items WHERE target_type = 'feed_item' AND target_id = feed_items.id) THEN 0
	WHEN EXISTS (SELECT 1 FROM feed_item_stars WHERE feed_item_id = feed_items.id) THEN 1
	ELSE 2
END, feed_items.created_at ASC, feed_items.published_at ASC
LIMIT ?
FOR UPDATE OF feed_items SKIP LOCKED`, sourceIDs, FullTextStatusPending, FullTextStatusRetry, now, limit).Scan(&candidates).Error
	if err != nil {
		return nil, fmt.Errorf("query additional full text claim candidates: %w", err)
	}
	return candidates, nil
}

func markFullTextItemsFetching(db *gorm.DB, items []model.FeedItem, now time.Time) ([]model.FeedItem, error) {
	if len(items) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	result := db.Model(&model.FeedItem{}).
		Where("id IN ? AND full_text_status IN ?", ids, []string{FullTextStatusPending, FullTextStatusRetry}).
		Where("next_full_text_attempt_at IS NULL OR next_full_text_attempt_at <= ?", now).
		Updates(map[string]any{
			"full_text_status":          FullTextStatusFetching,
			"full_text_attempt_count":   gorm.Expr("full_text_attempt_count + ?", 1),
			"last_full_text_attempt_at": &now,
			"next_full_text_attempt_at": nil,
		})
	if result.Error != nil {
		return nil, fmt.Errorf("mark full text items fetching: %w", result.Error)
	}

	claimed := make([]model.FeedItem, 0, result.RowsAffected)
	for _, item := range items {
		item.FullTextStatus = FullTextStatusFetching
		item.FullTextAttemptCount++
		item.LastFullTextAttemptAt = &now
		item.NextFullTextAttemptAt = nil
		claimed = append(claimed, item)
	}
	return claimed, nil
}

func releaseFullTextSourceLeases(db *gorm.DB, sourceIDs []uuid.UUID, leaseToken string) error {
	if len(sourceIDs) == 0 || leaseToken == "" {
		return nil
	}
	result := db.Model(&model.FeedSource{}).
		Where("id IN ? AND full_text_lease_token = ?", sourceIDs, leaseToken).
		Updates(map[string]any{"full_text_lease_token": "", "full_text_lease_until": nil})
	if result.Error != nil {
		return fmt.Errorf("release full text source leases: %w", result.Error)
	}
	return nil
}

func releaseFullTextSourceLease(db *gorm.DB, sourceID uuid.UUID, leaseToken string) error {
	return releaseFullTextSourceLeases(db, []uuid.UUID{sourceID}, leaseToken)
}
