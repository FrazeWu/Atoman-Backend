package feed

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	defaultDedupAttemptTimeout      = 2 * time.Minute
	defaultDedupRetryInitial        = 2 * time.Second
	defaultDedupRetryMaximum        = time.Minute
	defaultDedupFullRebuildInterval = 10 * time.Minute
)

// PublicDedupRefreshWorkerOptions applies only to a separately supervised,
// opt-in worker. Nothing in this package starts a worker in the API process.
type PublicDedupRefreshWorkerOptions struct {
	PollInterval      time.Duration
	AttemptTimeout    time.Duration
	RetryInitialDelay time.Duration
	RetryMaxDelay     time.Duration
	RefreshPolicy     PublicDedupRefreshPolicy
	// OnAttempt is an optional synchronous metrics/logging hook. It must
	// return promptly and must not do long-running work or mutate feed data.
	OnAttempt func(PublicDedupRefreshAttempt)
}

// PublicDedupRefreshAttempt contains one completed attempt and its next delay.
// Error may be ErrDedupSnapshotStale when an in-flight ingestion invalidated
// the attempt. Those conflicts are not counted as worker failures.
type PublicDedupRefreshAttempt struct {
	Result              PublicDedupRefreshResult
	Error               error
	Duration            time.Duration
	ConsecutiveFailures int
	NextDelay           time.Duration
}

func normalizeDedupWorkerOptions(opts PublicDedupRefreshWorkerOptions) (PublicDedupRefreshWorkerOptions, error) {
	if opts.PollInterval < time.Second {
		return opts, fmt.Errorf("dedup refresh poll interval must be >= 1s")
	}
	if opts.AttemptTimeout < 0 || opts.RetryInitialDelay < 0 || opts.RetryMaxDelay < 0 ||
		opts.RefreshPolicy.QuietPeriod < 0 || opts.RefreshPolicy.MinFullRebuildInterval < 0 {
		return opts, fmt.Errorf("negative dedup worker duration")
	}
	if opts.AttemptTimeout == 0 {
		opts.AttemptTimeout = defaultDedupAttemptTimeout
	}
	if opts.RetryInitialDelay == 0 {
		opts.RetryInitialDelay = defaultDedupRetryInitial
	}
	if opts.RetryMaxDelay == 0 {
		opts.RetryMaxDelay = defaultDedupRetryMaximum
	}
	if opts.RetryMaxDelay < opts.RetryInitialDelay {
		return opts, fmt.Errorf("dedup retry maximum must be >= initial delay")
	}
	if opts.RefreshPolicy.MinFullRebuildInterval == 0 {
		opts.RefreshPolicy.MinFullRebuildInterval = defaultDedupFullRebuildInterval
	}
	return opts, nil
}

func dedupRetryDelay(initial, maximum time.Duration, failures int) time.Duration {
	if failures <= 1 {
		return initial
	}
	delay := initial
	for i := 1; i < failures && delay < maximum; i++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func waitDedupRefresh(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// RunPublicRecentDedupRefreshWorker is an opt-in supervised loop. A transient
// query failure, timeout, or temporary database outage does not kill the worker.
// The parent context must be canceled by its supervisor during shutdown.
func (r *Repo) RunPublicRecentDedupRefreshWorker(ctx context.Context, options PublicDedupRefreshWorkerOptions) error {
	return runPublicRecentDedupRefreshWorker(ctx, options, r.refreshPublicRecentDedupIfQuiet)
}

func runPublicRecentDedupRefreshWorker(ctx context.Context, options PublicDedupRefreshWorkerOptions,
	refresh func(context.Context, PublicDedupRefreshPolicy) (PublicDedupRefreshResult, error)) error {
	options, err := normalizeDedupWorkerOptions(options)
	if err != nil {
		return err
	}
	if refresh == nil {
		return fmt.Errorf("missing dedup refresh function")
	}
	var failures int
	var nextDelay time.Duration
	for {
		if err := waitDedupRefresh(ctx, nextDelay); err != nil {
			return err
		}
		started := time.Now()
		attemptCtx, cancel := context.WithTimeout(ctx, options.AttemptTimeout)
		result, attemptErr := refresh(attemptCtx, options.RefreshPolicy)
		cancel()
		if err := ctx.Err(); err != nil {
			return err
		}
		switch {
		case errors.Is(attemptErr, ErrDedupSnapshotStale):
			// Concurrent ingestion changed the revision; retry at the normal
			// cadence, while the read path stays on the legacy fallback.
			result = PublicDedupRefreshConflict
			failures = 0
			nextDelay = options.PollInterval
		case attemptErr != nil:
			failures++
			nextDelay = dedupRetryDelay(options.RetryInitialDelay, options.RetryMaxDelay, failures)
		default:
			failures = 0
			nextDelay = options.PollInterval
		}
		if options.OnAttempt != nil {
			options.OnAttempt(PublicDedupRefreshAttempt{
				Result: result, Error: attemptErr,
				Duration: time.Since(started), ConsecutiveFailures: failures,
				NextDelay: nextDelay,
			})
		}
	}
}
