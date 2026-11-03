package feed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"atoman/internal/model"
	legacyfeed "atoman/internal/service"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// tryAppendOldestDedupItem only repairs one narrow, provably safe case:
// one committed INSERT of one visible FeedItem with a sort key strictly
// AFTER the former oldest item in the complete ordered source set. Its
// insertion cannot change earlier greedy representative assignments.
// Everything else returns applied=false and requires a full rebuild.
//
// Row and head edits are one PostgreSQL transaction, so a reader in the
// REPEATABLE READ snapshot sees either the old version or the new version.
func (r *Repo) tryAppendOldestDedupItem(ctx context.Context) (bool, error) {
	applied := false
	var appliedRevision string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var head feedDedupSnapshotHead
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("scope = ?", publicRecentDedupScope).Take(&head).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}

		previousTotal, err := strconv.ParseInt(head.Revision, 10, 64)
		if err != nil || previousTotal < 0 || len(head.SourceVersions) == 0 {
			return nil
		}
		previous := map[string]int64{}
		if err := json.Unmarshal(head.SourceVersions, &previous); err != nil {
			return err
		}
		reader := &Repo{db: tx}
		current, currentTotal, err := reader.dedupSourceCheckpoint(ctx)
		if err != nil {
			return err
		}
		currentTotalNum, err := strconv.ParseInt(currentTotal, 10, 64)
		if err != nil || currentTotalNum != previousTotal+1 {
			return nil
		}

		changedSource, sourceRevision, found := oneAdvancedSource(previous, current)
		if !found {
			return nil
		}
		var event feedDedupChangeEvent
		err = tx.Where("feed_source_id = ? AND revision = ?", changedSource, sourceRevision).Take(&event).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if event.Kind != "feed_items_insert" {
			return nil
		}
		var inserted []uuid.UUID
		if err := json.Unmarshal(event.ItemIDs, &inserted); err != nil {
			return err
		}
		if len(inserted) != 1 {
			return nil
		}
		var item model.FeedItem
		err = tx.Preload("FeedSource").Where("id = ?", inserted[0]).Take(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if item.FeedSource == nil || item.FeedSource.Hidden || item.FeedSourceID.String() != changedSource {
			return nil
		}
		if !dedupItemAfterTail(head, item) {
			return nil
		}

		normalizedURL := legacyfeed.NormalizeFeedItemURL(item.Link)
		matchQuery := tx.Where("generation_id = ?", head.GenerationID)
		if !item.PublishedAt.IsZero() {
			earliest := item.PublishedAt.Add(-72 * time.Hour)
			latest := item.PublishedAt.Add(72 * time.Hour)
			// Zero dates bypass the title time window in the legacy rule.
			// Equal normalized URLs always match even outside the window.
			predicate := "(published_at BETWEEN ? AND ? OR published_at < ?)"
			args := []any{earliest, latest, time.Date(2, 1, 1, 0, 0, 0, 0, time.UTC)}
			if normalizedURL != "" {
				predicate = "(published_at BETWEEN ? AND ? OR published_at < ? OR normalized_url = ?)"
				args = append(args, normalizedURL)
			}
			matchQuery = matchQuery.Where(predicate, args...)
		}
		var candidates []feedDedupSnapshotRow
		if err := matchQuery.Order("position ASC").Find(&candidates).Error; err != nil {
			return err
		}
		var matching *feedDedupSnapshotRow
		for i := range candidates {
			row := &candidates[i]
			if legacyfeed.FeedItemMatchesDuplicateRepresentative(row.NormalizedURL, row.NormalizedTitle, row.PublishedAt, item) {
				matching = row
				break
			}
		}

		newTotal := head.Total
		if matching == nil {
			ids, err := json.Marshal([]uuid.UUID{item.ID})
			if err != nil {
				return err
			}
			sources := json.RawMessage("null")
			row := feedDedupSnapshotRow{
				GenerationID: head.GenerationID, Position: head.PositionBase + head.Total, FeedItemID: item.ID,
				PublishedAt: item.PublishedAt, NormalizedURL: normalizedURL,
				NormalizedTitle: legacyfeed.NormalizeFeedItemTitle(item.Title),
				DuplicateCount:  1, DuplicateMemberIDs: ids, DuplicateSources: sources,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			newTotal++
			if err := tx.Model(&feedDedupSnapshotGeneration{}).
				Where("generation_id = ?", head.GenerationID).Update("total", newTotal).Error; err != nil {
				return err
			}
		} else {
			var ids []uuid.UUID
			if err := json.Unmarshal(matching.DuplicateMemberIDs, &ids); err != nil {
				return err
			}
			if len(ids) != matching.DuplicateCount {
				return ErrDedupSnapshotStale
			}
			ids = append(ids, item.ID)
			idsJSON, err := json.Marshal(ids)
			if err != nil {
				return err
			}
			var sources []string
			if err := json.Unmarshal(matching.DuplicateSources, &sources); err != nil {
				return err
			}
			if matching.DuplicateCount == 1 {
				var primary model.FeedItem
				if err := tx.Preload("FeedSource").Where("id = ?", matching.FeedItemID).Take(&primary).Error; err != nil {
					return err
				}
				sources = append(sources, legacyfeed.FeedItemDuplicateSourceTitle(primary))
			}
			sources = append(sources, legacyfeed.FeedItemDuplicateSourceTitle(item))
			sort.Strings(sources)
			unique := sources[:0]
			for _, source := range sources {
				if len(unique) == 0 || unique[len(unique)-1] != source {
					unique = append(unique, source)
				}
			}
			sourcesJSON, err := json.Marshal(unique)
			if err != nil {
				return err
			}
			if err := tx.Model(&feedDedupSnapshotRow{}).
				Where("generation_id = ? AND position = ?", matching.GenerationID, matching.Position).
				Updates(map[string]any{
					"duplicate_count":      matching.DuplicateCount + 1,
					"duplicate_member_ids": idsJSON,
					"duplicate_sources":    sourcesJSON,
				}).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&feedDedupSnapshotMember{
			GenerationID: head.GenerationID, FeedItemID: item.ID, PublishedAt: item.PublishedAt,
			NormalizedURL:     normalizedURL,
			NormalizedURLHash: feedDedupURLHash(normalizedURL),
			NormalizedTitle:   legacyfeed.NormalizeFeedItemTitle(item.Title),
		}).Error; err != nil {
			return err
		}
		encodedVersions, err := json.Marshal(current)
		if err != nil {
			return err
		}
		updates := map[string]any{
			"revision": currentTotal, "source_versions": encodedVersions,
			"tail_published_at": item.PublishedAt,
			"tail_feed_item_id": item.ID,
			"total":             newTotal,
			"raw_item_count":    head.RawItemCount + 1,
		}
		if head.RawItemCount == 0 {
			updates["first_published_at"] = item.PublishedAt
			updates["first_feed_item_id"] = item.ID
		}
		if err := tx.Model(&feedDedupSnapshotHead{}).Where("scope = ?", publicRecentDedupScope).
			Updates(updates).Error; err != nil {
			return err
		}
		appliedRevision = currentTotal
		applied = true
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

func oneAdvancedSource(before, after map[string]int64) (string, int64, bool) {
	for sourceID := range before {
		if _, ok := after[sourceID]; !ok {
			return "", 0, false
		}
	}
	changed := ""
	var newRevision int64
	for sourceID, value := range after {
		diff := value - before[sourceID]
		if diff == 1 {
			if changed != "" {
				return "", 0, false
			}
			changed, newRevision = sourceID, value
		} else if diff != 0 {
			return "", 0, false
		}
	}
	return changed, newRevision, changed != ""
}

func dedupItemAfterTail(head feedDedupSnapshotHead, item model.FeedItem) bool {
	// PostgreSQL sorts NULL timestamps FIRST in DESC order. A zero Go
	// timestamp can represent a NULL database date, not an oldest item.
	if item.PublishedAt.IsZero() {
		return false
	}
	if head.TailPublishedAt != nil && head.TailPublishedAt.IsZero() {
		return false
	}
	if head.TailPublishedAt == nil {
		return head.Total == 0
	}
	if item.PublishedAt.Before(*head.TailPublishedAt) {
		return true
	}
	if item.PublishedAt.After(*head.TailPublishedAt) {
		return false
	}
	return item.ID.String() < head.TailFeedItemID.String()
}
