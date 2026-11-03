package feed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// PublicDedupRefreshPolicy controls an *opt-in* rebuild worker, not an API
// handler. It is intentionally not registered in server startup or migrations.
// Continuous ingestion may keep the projection stale; the read path must
// continue to reject stale generations rather than return incomplete data.
type PublicDedupRefreshPolicy struct {
	QuietPeriod time.Duration
	// MinFullRebuildInterval limits expensive full generations, but never
	// blocks safe incremental updates. Zero disables it for direct test calls;
	// the supervised worker supplies a nonzero default.
	MinFullRebuildInterval time.Duration
}

type PublicDedupRefreshResult string

const (
	PublicDedupRefreshCurrent     PublicDedupRefreshResult = "current"
	PublicDedupRefreshDeferred    PublicDedupRefreshResult = "deferred"
	PublicDedupRefreshBusy        PublicDedupRefreshResult = "busy"
	PublicDedupRefreshRebuilt     PublicDedupRefreshResult = "rebuilt"
	PublicDedupRefreshIncremental PublicDedupRefreshResult = "incremental"
	PublicDedupRefreshConflict    PublicDedupRefreshResult = "conflict"
)

// These advisory keys only serialize competing rebuild workers. RSS writers
// never acquire this lock, unlike the former global revision-counter row.
const dedupRefreshLockClass int32 = 0x46454544
const dedupRefreshLockObject int32 = 0x44555031

func (r *Repo) currentPublicRecentDedupRefreshState(ctx context.Context, policy PublicDedupRefreshPolicy) (string, PublicDedupRefreshResult, error) {
	revision, err := r.dedupSourceRevision(ctx)
	if err != nil {
		return "", "", err
	}
	var head feedDedupSnapshotHead
	err = r.db.WithContext(ctx).Where("scope = ?", publicRecentDedupScope).Take(&head).Error
	if err == nil && head.Revision == revision {
		return revision, PublicDedupRefreshCurrent, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", "", err
	}
	if policy.QuietPeriod > 0 {
		var lastChange sql.NullTime
		if err := r.db.WithContext(ctx).Raw("SELECT MAX(updated_at) FROM feed_dedup_source_revisions").Scan(&lastChange).Error; err != nil {
			return "", "", err
		}
		if lastChange.Valid && time.Since(lastChange.Time) < policy.QuietPeriod {
			return revision, PublicDedupRefreshDeferred, nil
		}
	}
	return revision, "", nil
}

// refreshPublicRecentDedupIfQuiet checks the revision before and after taking
// a session advisory lock. It coalesces short bursts of ingestion and avoids
// concurrent full rebuilds. It first attempts provably safe newest-prefix or
// oldest-tail updates and falls back to a full rebuild for all other changes.
func (r *Repo) refreshPublicRecentDedupIfQuiet(ctx context.Context, policy PublicDedupRefreshPolicy) (PublicDedupRefreshResult, error) {
	if policy.QuietPeriod < 0 || policy.MinFullRebuildInterval < 0 {
		return "", fmt.Errorf("negative dedup refresh policy duration")
	}
	_, state, err := r.currentPublicRecentDedupRefreshState(ctx, policy)
	if err != nil || state != "" {
		return state, err
	}
	sqlDB, err := r.db.DB()
	if err != nil {
		return "", err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	var locked bool
	err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1, $2)", dedupRefreshLockClass, dedupRefreshLockObject).Scan(&locked)
	if err != nil {
		return "", err
	}
	if !locked {
		return PublicDedupRefreshBusy, nil
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var released bool
		_ = conn.QueryRowContext(releaseCtx, "SELECT pg_advisory_unlock($1, $2)", dedupRefreshLockClass, dedupRefreshLockObject).Scan(&released)
	}()
	_, state, err = r.currentPublicRecentDedupRefreshState(ctx, policy)
	if err != nil || state != "" {
		return state, err
	}
	applied, err := r.tryPrependIsolatedRecentBatch(ctx)
	if err != nil {
		return "", err
	}
	if applied {
		return PublicDedupRefreshIncremental, nil
	}
	applied, err = r.tryAppendOldestDedupItem(ctx)
	if err != nil {
		return "", err
	}
	if applied {
		return PublicDedupRefreshIncremental, nil
	}
	// Keep the existing snapshot stale (so future readers must use their
	// legacy fallback) instead of rebuilding repeatedly during RSS bursts.
	if policy.MinFullRebuildInterval > 0 {
		var head feedDedupSnapshotHead
		err := r.db.WithContext(ctx).Where("scope = ?", publicRecentDedupScope).Take(&head).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", err
		}
		if err == nil && time.Since(head.BuiltAt) < policy.MinFullRebuildInterval {
			return PublicDedupRefreshDeferred, nil
		}
	}
	if _, err := r.publishPublicRecentDedupSnapshotTracked(ctx); err != nil {
		return "", err
	}
	return PublicDedupRefreshRebuilt, nil
}

// RunPublicRecentDedupRefreshLoop remains the compatibility entry point for
// an opt-in, standalone worker. It is NOT registered by the HTTP server.
// The supervised implementation retries recoverable failures and enforces a
// minimum full-rebuild interval even when the caller leaves it unset.
func (r *Repo) RunPublicRecentDedupRefreshLoop(ctx context.Context, pollInterval time.Duration, policy PublicDedupRefreshPolicy) error {
	return r.RunPublicRecentDedupRefreshWorker(ctx, PublicDedupRefreshWorkerOptions{
		PollInterval: pollInterval, RefreshPolicy: policy,
	})
}
