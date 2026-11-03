package feed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"time"

	"atoman/internal/model"
	legacyfeed "atoman/internal/service"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxSafePrefixBatch = 24
const maxPrefixCandidateRows = 25000
const maxPrefixCandidateChecks = 100000
const titleWindow = 72 * time.Hour

// tryPrependIsolatedRecentBatch safely handles only a new globally ordered
// prefix of one or more items that is PROVEN disjoint from every old raw item.
// Greedy representative clustering can change anywhere when a newest item
// matches an old hidden member, so querying only old representatives is unsafe.
// The raw-item snapshot index is mandatory and is verified before use.
// When any proof fails, return (false,nil): the caller performs a full rebuild.
func (r *Repo) tryPrependIsolatedRecentBatch(ctx context.Context) (bool, error) {
	applied := false
	var appliedRevision string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var head feedDedupSnapshotHead
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("scope = ?", publicRecentDedupScope).Take(&head).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if head.RawItemCount <= 0 || head.Total <= 0 || head.FirstPublishedAt == nil || head.FirstPublishedAt.IsZero() || head.FirstFeedItemID == uuid.Nil {
			return nil
		}
		if head.PositionBase <= math.MinInt64+maxSafePrefixBatch || len(head.SourceVersions) == 0 {
			return nil
		}
		var indexedCount int64
		if err := tx.Model(&feedDedupSnapshotMember{}).Where("generation_id = ?", head.GenerationID).Count(&indexedCount).Error; err != nil {
			return err
		}
		if indexedCount != head.RawItemCount {
			return nil
		}

		baseline, err := strconv.ParseInt(head.Revision, 10, 64)
		if err != nil || baseline < 0 {
			return nil
		}
		previous := map[string]int64{}
		if err := json.Unmarshal(head.SourceVersions, &previous); err != nil {
			return err
		}
		reader := &Repo{db: tx}
		current, currentRevision, err := reader.dedupSourceCheckpoint(ctx)
		if err != nil {
			return err
		}
		revisionNum, err := strconv.ParseInt(currentRevision, 10, 64)
		if err != nil || revisionNum != baseline+1 {
			return nil
		}
		sourceID, changedRevision, ok := oneAdvancedSource(previous, current)
		if !ok {
			return nil
		}
		var event feedDedupChangeEvent
		err = tx.Where("feed_source_id = ? AND revision = ?", sourceID, changedRevision).Take(&event).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if event.Kind != "feed_items_insert" {
			return nil
		}
		var ids []uuid.UUID
		if err := json.Unmarshal(event.ItemIDs, &ids); err != nil {
			return err
		}
		if len(ids) < 1 || len(ids) > maxSafePrefixBatch {
			return nil
		}
		var items []model.FeedItem
		if err := tx.Preload("FeedSource").Where("id IN ?", ids).Find(&items).Error; err != nil {
			return err
		}
		if len(items) != len(ids) {
			return nil
		}
		for _, item := range items {
			if item.FeedSource == nil || item.FeedSource.Hidden || item.FeedSourceID.String() != sourceID || item.PublishedAt.IsZero() {
				return nil
			}
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].PublishedAt.Equal(items[j].PublishedAt) {
				return items[i].ID.String() > items[j].ID.String()
			}
			return items[i].PublishedAt.After(items[j].PublishedAt)
		})
		if !dedupItemBeforeFirst(head, items[len(items)-1]) {
			return nil
		}

		// Candidate lookup includes the title's 72h comparison window, all
		// zero-time legacy items, and the URL hash across ALL dates.
		// Its bounding limit is fail-closed, never a silent LIMIT of results.
		var compared int
		for _, item := range items {
			normalizedURL := legacyfeed.NormalizeFeedItemURL(item.Link)
			normalizedTitle := legacyfeed.NormalizeFeedItemTitle(item.Title)
			windowStart, windowEnd := item.PublishedAt.Add(-titleWindow), item.PublishedAt.Add(titleWindow)
			query := tx.Model(&feedDedupSnapshotMember{}).Where("generation_id = ?", head.GenerationID)
			if normalizedURL == "" {
				query = query.Where("(published_at BETWEEN ? AND ? OR published_at = ?)", windowStart, windowEnd, time.Time{})
			} else {
				query = query.Where("(published_at BETWEEN ? AND ? OR published_at = ? OR normalized_url_hash = ?)",
					windowStart, windowEnd, time.Time{}, feedDedupURLHash(normalizedURL))
			}
			var possible []feedDedupSnapshotMember
			if err := query.Limit(maxPrefixCandidateRows + 1).Find(&possible).Error; err != nil {
				return err
			}
			compared += len(possible)
			if len(possible) > maxPrefixCandidateRows || compared > maxPrefixCandidateChecks {
				return nil
			}
			for _, old := range possible {
				if legacyfeed.FeedNormalizedItemsMatch(normalizedURL, normalizedTitle, item.PublishedAt,
					old.NormalizedURL, old.NormalizedTitle, old.PublishedAt) {
					return nil
				}
			}
		}

		legacyfeed.AnnotateDuplicateFeedItems(items)
		representatives := make([]model.FeedItem, 0, len(items))
		for _, item := range items {
			if !item.IsDuplicate {
				representatives = append(representatives, item)
			}
		}
		if len(representatives) == 0 {
			return nil
		}
		groupCount := int64(len(representatives))
		newBase := head.PositionBase - groupCount
		for index, item := range representatives {
			idsJSON, err := json.Marshal(item.DuplicateItemIDs)
			if err != nil {
				return err
			}
			sourceJSON, err := json.Marshal(item.DuplicateSources)
			if err != nil {
				return err
			}
			row := feedDedupSnapshotRow{
				GenerationID: head.GenerationID, Position: newBase + int64(index),
				FeedItemID: item.ID, PublishedAt: item.PublishedAt,
				NormalizedURL:      legacyfeed.NormalizeFeedItemURL(item.Link),
				NormalizedTitle:    legacyfeed.NormalizeFeedItemTitle(item.Title),
				DuplicateCount:     item.DuplicateCount,
				DuplicateMemberIDs: idsJSON, DuplicateSources: sourceJSON,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		members := make([]feedDedupSnapshotMember, 0, len(items))
		for _, item := range items {
			url := legacyfeed.NormalizeFeedItemURL(item.Link)
			members = append(members, feedDedupSnapshotMember{
				GenerationID: head.GenerationID, FeedItemID: item.ID, PublishedAt: item.PublishedAt,
				NormalizedURL: url, NormalizedURLHash: feedDedupURLHash(url),
				NormalizedTitle: legacyfeed.NormalizeFeedItemTitle(item.Title),
			})
		}
		if err := tx.Create(members).Error; err != nil {
			return err
		}
		newTotal := head.Total + groupCount
		if err := tx.Model(&feedDedupSnapshotGeneration{}).
			Where("generation_id = ?", head.GenerationID).Update("total", newTotal).Error; err != nil {
			return err
		}
		versionsJSON, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if err := tx.Model(&feedDedupSnapshotHead{}).Where("scope = ?", publicRecentDedupScope).
			Updates(map[string]any{
				"revision": currentRevision, "source_versions": versionsJSON,
				"position_base": newBase, "total": newTotal,
				"raw_item_count":     head.RawItemCount + int64(len(items)),
				"first_published_at": items[0].PublishedAt,
				"first_feed_item_id": items[0].ID,
			}).Error; err != nil {
			return err
		}
		applied = true
		appliedRevision = currentRevision
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return false, err
	}
	if !applied {
		return false, nil
	}
	latest, err := r.dedupSourceRevision(ctx)
	if err != nil {
		return false, err
	}
	if latest != appliedRevision {
		return false, ErrDedupSnapshotStale
	}
	return true, nil
}

func dedupItemBeforeFirst(head feedDedupSnapshotHead, item model.FeedItem) bool {
	if head.FirstPublishedAt == nil {
		return false
	}
	if item.PublishedAt.After(*head.FirstPublishedAt) {
		return true
	}
	if item.PublishedAt.Before(*head.FirstPublishedAt) {
		return false
	}
	return item.ID.String() > head.FirstFeedItemID.String()
}
