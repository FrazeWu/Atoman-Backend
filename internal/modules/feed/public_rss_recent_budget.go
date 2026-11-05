package feed

import (
	"context"
	"net/http"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
)

// Isolated HTTP-only safety budget. The default full-corpus legacy route is
// never used on a production request path.
const rssLegacyRequestTimeout = 1500 * time.Millisecond
const rssLegacyConcurrentScans = 2

// A non-blocking cap prevents a stale snapshot from spawning unlimited
// concurrent deduplication scans in the HTTP process.
var rssLegacyScanSlots = make(chan struct{}, rssLegacyConcurrentScans)

func rssLegacyUnavailable(message string) error {
	return apperr.New(http.StatusServiceUnavailable,
		"feed.rss_snapshot_unavailable", message, nil)
}

func (r *Repo) listPublicRSSRecentWithinRequestBudget(
	ctx context.Context, page, size int,
) ([]model.FeedItem, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	select {
	case rssLegacyScanSlots <- struct{}{}:
		defer func() { <-rssLegacyScanSlots }()
	default:
		return nil, 0, rssLegacyUnavailable("RSS fallback is busy; retry after the snapshot is refreshed")
	}
	boundedCtx, cancel := context.WithTimeout(ctx, rssLegacyRequestTimeout)
	defer cancel()
	items, total, err := r.listPublicRSSRecentLegacy(boundedCtx, page, size)
	if parentErr := ctx.Err(); parentErr != nil {
		return nil, 0, parentErr
	}
	if boundedCtx.Err() == context.DeadlineExceeded {
		return nil, 0, rssLegacyUnavailable("RSS fallback exceeded its request time budget")
	}
	return items, total, err
}
