package feed

import (
	"atoman/internal/platform/apperr"
	"context"
	"math"
	"net/http"
	"os"
	"strconv"

	"atoman/internal/model"
	legacyfeed "atoman/internal/service"
)

// listPublicRSSRecentLegacy reproduces the RSS-only projection input with no
// posts, subscriptions, hidden sources, read state or user-specific filters.
// Keep it independent of snapshot tables so it works during rollback.
func (r *Repo) listPublicRSSRecentLegacy(ctx context.Context, page, size int) ([]model.FeedItem, int64, error) {
	page, size = normalizedPage(page), normalizedPageSize(size)
	// The isolated HTTP experiment must never load an unbounded RSS corpus
	// after a missing/stale snapshot. Production routes do not use this reader.
	const defaultMaxItems = 10000
	maxItems := defaultMaxItems
	if os.Getenv("FEED_SCALE_ISOLATED") == "yes" {
		if override, err := strconv.Atoi(os.Getenv("FEED_RSS_LEGACY_MAX_ITEMS")); err == nil && override > 0 && override <= defaultMaxItems {
			maxItems = override
		}
	}
	var scans []model.FeedItem
	if err := r.db.WithContext(ctx).Model(&model.FeedItem{}).
		Select(feedDuplicateScanColumns).Preload("FeedSource").
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_sources.hidden = ? AND feed_sources.deleted_at IS NULL AND feed_sources.source_type = ?", false, "external_rss").
		Order("feed_items.published_at DESC, feed_items.id DESC").
		Limit(maxItems + 1).
		Find(&scans).Error; err != nil {
		return nil, 0, err
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if len(scans) > maxItems {
		return nil, 0, apperr.New(http.StatusServiceUnavailable, "feed.rss_snapshot_unavailable", "RSS snapshot unavailable; full-scan fallback exceeds the isolated request budget", nil)
	}
	legacyfeed.AnnotateDuplicateFeedItems(scans)
	representatives := make([]model.FeedItem, 0, len(scans))
	for i := range scans {
		if !scans[i].IsDuplicate {
			representatives = append(representatives, scans[i])
		}
	}
	total := int64(len(representatives))
	if int64(page-1) > math.MaxInt64/int64(size) {
		return []model.FeedItem{}, total, nil
	}
	start := int64(page-1) * int64(size)
	if start >= total {
		return []model.FeedItem{}, total, nil
	}
	end := start + int64(size)
	if end > total {
		end = total
	}
	selected := representatives[int(start):int(end)]
	ids := make([]model.FeedItem, 0, len(selected))
	for _, item := range selected {
		ids = append(ids, item)
	}
	keys := make([]string, 0, len(ids))
	for _, item := range ids {
		keys = append(keys, item.ID.String())
	}
	var hydrated []model.FeedItem
	if err := r.db.WithContext(ctx).Preload("FeedSource").Where("id IN ?", keys).Find(&hydrated).Error; err != nil {
		return nil, 0, err
	}
	byID := make(map[string]model.FeedItem, len(hydrated))
	for _, item := range hydrated {
		byID[item.ID.String()] = item
	}
	out := make([]model.FeedItem, 0, len(selected))
	for _, item := range selected {
		full, exists := byID[item.ID.String()]
		if !exists {
			return nil, 0, ErrDedupSnapshotStale
		}
		full.IsDuplicate = item.IsDuplicate
		full.DuplicateCount = item.DuplicateCount
		full.DuplicateOfID = item.DuplicateOfID
		full.DuplicateItemIDs = item.DuplicateItemIDs
		full.DuplicateSources = item.DuplicateSources
		out = append(out, full)
	}
	return out, total, nil
}

func publicRSSDTO(items []model.FeedItem) []TimelineItemDTO {
	output := make([]TimelineItemDTO, 0, len(items))
	for i := range items {
		output = append(output, TimelineItemDTO{Type: timelineFeedItemType(&items[i]), FeedItem: &items[i], PublishedAt: items[i].PublishedAt})
	}
	return output
}
