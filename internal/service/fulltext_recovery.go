package service

import (
	"fmt"
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func recoverStaleFullTextFetches(db *gorm.DB, now time.Time) error {
	staleBefore := now.Add(-fullTextStaleFetchAfter)
	err := db.Transaction(func(tx *gorm.DB) error {
		var staleItems []model.FeedItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Select("id", "feed_source_id", "full_text_attempt_count").
			Where("full_text_status = ?", FullTextStatusFetching).
			Where("last_full_text_attempt_at IS NULL OR last_full_text_attempt_at <= ?", staleBefore).
			Find(&staleItems).Error; err != nil {
			return fmt.Errorf("lock stale full text fetches: %w", err)
		}
		if len(staleItems) == 0 {
			return nil
		}

		firstRetryAt, _ := CalculateNextFullTextRetryAt(now, 1)
		secondRetryAt, _ := CalculateNextFullTextRetryAt(now, 2)
		thirdRetryAt, _ := CalculateNextFullTextRetryAt(now, 3)
		itemIDs := make([]uuid.UUID, 0, len(staleItems))
		sourceFailureCounts := make(map[uuid.UUID]int, len(staleItems))
		diagnostics := make([]model.FeedSourceDiagnostic, 0, len(staleItems))
		for _, item := range staleItems {
			itemIDs = append(itemIDs, item.ID)
			sourceFailureCounts[item.FeedSourceID]++
			itemID := item.ID
			diagnostics = append(diagnostics, model.FeedSourceDiagnostic{
				FeedSourceID: item.FeedSourceID,
				FeedItemID:   &itemID,
				Kind:         "failure",
				ErrorCode:    FullTextErrorRequestTimeout,
				Message:      "stale full text fetch recovered",
				AttemptCount: item.FullTextAttemptCount,
			})
		}

		if err := tx.Model(&model.FeedItem{}).
			Where("id IN ? AND full_text_status = ?", itemIDs, FullTextStatusFetching).
			Updates(map[string]any{
				"full_text_status": gorm.Expr(`CASE
					WHEN full_text_attempt_count IN (?, ?, ?) THEN ?
					ELSE ?
				END`, 1, 2, 3, FullTextStatusRetry, FullTextStatusFailed),
				"full_text_error_code": FullTextErrorRequestTimeout,
				"full_text_error":      "stale full text fetch recovered",
				"next_full_text_attempt_at": gorm.Expr(`CASE full_text_attempt_count
					WHEN ? THEN ?::TIMESTAMPTZ
					WHEN ? THEN ?::TIMESTAMPTZ
					WHEN ? THEN ?::TIMESTAMPTZ
					ELSE NULL
				END`, 1, firstRetryAt, 2, secondRetryAt, 3, thirdRetryAt),
			}).Error; err != nil {
			return fmt.Errorf("recover stale full text items: %w", err)
		}
		if err := incrementStaleFullTextSourceFailures(tx, sourceFailureCounts, now); err != nil {
			return fmt.Errorf("recover stale full text source failures: %w", err)
		}
		if err := tx.CreateInBatches(&diagnostics, 100).Error; err != nil {
			return fmt.Errorf("record stale full text diagnostics: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("recover stale full text fetches: %w", err)
	}
	return nil
}

func incrementStaleFullTextSourceFailures(db *gorm.DB, counts map[uuid.UUID]int, now time.Time) error {
	if len(counts) == 0 {
		return nil
	}

	var values strings.Builder
	args := []any{now, FullTextErrorRequestTimeout, "stale full text fetch recovered", now}
	for sourceID, count := range counts {
		if values.Len() > 0 {
			values.WriteString(", ")
		}
		values.WriteString("(?::uuid, ?::integer)")
		args = append(args, sourceID, count)
	}
	query := `
UPDATE feed_sources AS sources
SET full_text_failure_count = sources.full_text_failure_count + failures.count,
	full_text_consecutive_failure_count = sources.full_text_consecutive_failure_count + failures.count,
	full_text_last_failure_at = ?,
	full_text_last_error_code = ?,
	full_text_last_error = ?,
	updated_at = ?
FROM (VALUES ` + values.String() + `) AS failures(id, count)
WHERE sources.id = failures.id`
	if err := db.Exec(query, args...).Error; err != nil {
		return fmt.Errorf("increment stale full text source failures: %w", err)
	}
	return nil
}
