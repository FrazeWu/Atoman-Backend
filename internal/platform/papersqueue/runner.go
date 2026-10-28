package papersqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"atoman/internal/platform/paperssandbox"
)

// SandboxExecutor is the boundary to a privileged launcher. The real launcher
// MUST run Supervisor.Run (including verified cleanup) and never return a
// misleading success result before all owned resources are gone.
type SandboxExecutor interface {
	Run(context.Context, Claim) (paperssandbox.Result, error)
	Reap(context.Context, Claim) error
}

type Runner struct {
	Queue    *Queue
	Executor SandboxExecutor
}

// Step processes at most one transactionally claimed operation. No HTTP
// request starts this directly: a future separately authorized worker service
// owns the polling loop and platform sandbox credentials.
func (r *Runner) Step(ctx context.Context, workerID string) (bool, error) {
	if r == nil || r.Queue == nil || r.Executor == nil {
		return false, ErrNotReady
	}
	claim, ok, err := r.Queue.ClaimNext(ctx, workerID)
	if err != nil || !ok {
		return ok, err
	}
	runningCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	stopped := make(chan struct{})
	var guard sync.Mutex
	var heartbeatErr error
	setError := func(err error) {
		guard.Lock()
		if heartbeatErr == nil {
			heartbeatErr = err
		}
		guard.Unlock()
	}
	period := r.Queue.lease / 3
	if period < time.Second {
		period = time.Second
	}
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-runningCtx.Done():
				return
			case <-ticker.C:
				timedCtx, stop := context.WithTimeout(context.Background(), period)
				e := r.Queue.Heartbeat(timedCtx, claim)
				if e == nil && claim.Kind == ClaimRun {
					var requested bool
					requested, e = r.Queue.CancellationRequested(timedCtx, claim)
					if e == nil && requested {
						e = context.Canceled
					}
				}
				stop()
				if e != nil {
					setError(e)
					cancel()
					return
				}
			}
		}
	}()
	// Claim ownership is held through cleanup and commit. Renewal continues
	// while cleanup is underway, not only while the sandbox process is active.
	var operationErr error
	if claim.Kind == ClaimRecover {
		cleanupErr := r.Executor.Reap(runningCtx, claim)
		if cleanupErr != nil {
			operationErr = fmt.Errorf("orphan cleanup: %w", cleanupErr)
		}
		close(done)
		<-stopped
		confirmCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		commitErr := r.Queue.FinishRecovery(confirmCtx, claim, cleanupErr == nil)
		stop()
		operationErr = errors.Join(operationErr, commitErr)
	} else {
		result, runErr := r.Executor.Run(runningCtx, claim)
		if runErr != nil {
			operationErr = fmt.Errorf("sandbox run: %w", runErr)
		}
		close(done)
		<-stopped
		confirmCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		commitErr := r.Queue.Complete(confirmCtx, claim, result)
		stop()
		operationErr = errors.Join(operationErr, commitErr)
	}
	cancel()
	guard.Lock()
	renewalErr := heartbeatErr
	guard.Unlock()
	return true, errors.Join(operationErr, renewalErr)
}
