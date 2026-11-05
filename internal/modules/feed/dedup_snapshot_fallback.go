package feed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"atoman/internal/model"
)

// PublicRecentDedupLegacyReader must implement the EXISTING RSS-only/recent
// duplicate-filter contract. Callers must ensure that query shape is eligible
// before opting in to the projection; mixed timelines and filters are NOT
// supported by this snapshot, and must stay on their original code paths.
type PublicRecentDedupLegacyReader func(context.Context, int, int) ([]model.FeedItem, int64, error)

// readPublicRecentDedupPageWithFallback never returns a partial projection on
// any projection read error. It runs the projection under a separate, bounded
// child context, leaving the original parent context available for the legacy
// query if the projection is stale, unavailable, or times out. The isolated
// RSS-only route supplies a bounded legacy reader; mixed routes never opt in.
func (r *Repo) readPublicRecentDedupPageWithFallback(ctx context.Context, page, pageSize int,
	projectionTimeout time.Duration, legacy PublicRecentDedupLegacyReader,
	onFallback func(error)) ([]model.FeedItem, int64, error) {
	if legacy == nil || projectionTimeout <= 0 {
		return nil, 0, fmt.Errorf("projection fallback requires legacy reader and positive timeout")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	projectionCtx, cancel := context.WithTimeout(ctx, projectionTimeout)
	projected, total, projectionErr := r.readPublicRecentDedupSnapshotPageTracked(projectionCtx, page, pageSize)
	cancel()
	if projectionErr == nil {
		return projected, total, nil
	}
	// A client cancellation is NOT a reason to start an expensive legacy scan.
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if onFallback != nil {
		onFallback(projectionErr)
	}
	old, oldTotal, oldErr := legacy(ctx, page, pageSize)
	if oldErr != nil {
		return nil, 0, fmt.Errorf("projection and legacy feed reads failed: %w",
			errors.Join(projectionErr, oldErr))
	}
	return old, oldTotal, nil
}
