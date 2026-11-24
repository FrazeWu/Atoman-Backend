package paperssandbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Backend has one obligation the control plane cannot infer: Cleanup must
// return nil ONLY after runsc list, the dedicated systemd scope and all owned
// namespace mounts have been checked and no owned resources remain.
type Backend interface {
	Execute(context.Context, Job) (Evidence, error)
	Cleanup(context.Context, Job) error
}

// Supervisor limits this process to one heavy task at a time. This is NOT the
// eventual cross-process PostgreSQL queue: production must additionally use
// leases and fencing tokens before starting a sandbox.
type Supervisor struct {
	mu             sync.Mutex
	backend        Backend
	busy           bool
	quarantined    bool
	cleanupTimeout time.Duration
}

func NewSupervisor(backend Backend) *Supervisor {
	return &Supervisor{backend: backend, cleanupTimeout: 10 * time.Second}
}

func (s *Supervisor) Quarantined() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quarantined
}

func (s *Supervisor) Run(ctx context.Context, job Job) (Result, error) {
	if err := job.Validate(); err != nil {
		return Result{}, err
	}
	if s == nil || s.backend == nil {
		return Result{}, ErrInvalidJob
	}
	s.mu.Lock()
	if s.quarantined {
		s.mu.Unlock()
		return Result{}, ErrQuarantined
	}
	if s.busy {
		s.mu.Unlock()
		return Result{}, ErrBusy
	}
	s.busy = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()

	result := Result{JobID: job.ID, StartedAt: time.Now().UTC()}
	runCtx, cancel := context.WithTimeout(ctx, job.Timeout)
	evidence, runErr := s.backend.Execute(runCtx, job)
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		evidence.DeadlineReached = true
	} else if errors.Is(runCtx.Err(), context.Canceled) {
		evidence.ContextCanceled = true
	}
	cancel()

	result.UnderlyingStatus = Classify(evidence, runErr)
	result.Status = result.UnderlyingStatus
	result.ExitCode = evidence.ExitCode
	result.ExitKnown = evidence.ExitKnown
	result.SentryObserved = evidence.SentryObserved
	result.LimitsVerified = evidence.LimitsVerified
	result.ArtifactVerified = evidence.ArtifactVerified
	result.Events = evidence.Events

	// The user's cancellation/deadline must never skip privileged cleanup.
	cleanupCtx, stopCleanup := context.WithTimeout(context.Background(), s.cleanupTimeout)
	cleanupErr := s.backend.Cleanup(cleanupCtx, job)
	stopCleanup()
	result.CleanupVerified = cleanupErr == nil

	if cleanupErr != nil {
		result.Status = CleanupFailed
		result.FailureDetail = "sandbox cleanup could not be verified; new tasks blocked"
		s.mu.Lock()
		s.quarantined = true
		s.mu.Unlock()
	} else if result.Status != Completed {
		result.FailureDetail = string(result.Status)
	}
	result.FinishedAt = time.Now().UTC()
	return result, nil
}

// RecoverOrphans is a P0 restart hook. A future durable task queue will
// supply job IDs whose worker leases expired; none may be rescheduled until
// every abandoned sandbox has been positively cleaned and verified.
func (s *Supervisor) RecoverOrphans(ctx context.Context, orphans []Job) error {
	if s == nil || s.backend == nil {
		return ErrInvalidJob
	}
	for _, job := range orphans {
		if err := job.Validate(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	if s.quarantined {
		s.mu.Unlock()
		return ErrQuarantined
	}
	if s.busy {
		s.mu.Unlock()
		return ErrBusy
	}
	s.busy = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()
	for _, job := range orphans {
		if err := ctx.Err(); err != nil {
			s.mu.Lock()
			s.quarantined = true
			s.mu.Unlock()
			return fmt.Errorf("%w: orphan recovery interrupted: %v", ErrQuarantined, err)
		}
		cleanCtx, stop := context.WithTimeout(context.Background(), s.cleanupTimeout)
		err := s.backend.Cleanup(cleanCtx, job)
		stop()
		if err != nil {
			s.mu.Lock()
			s.quarantined = true
			s.mu.Unlock()
			return fmt.Errorf("%w: orphan %s cleanup: %v", ErrQuarantined, job.ID, err)
		}
	}
	return nil
}
