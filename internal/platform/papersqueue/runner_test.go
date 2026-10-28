package papersqueue

import (
	"context"
	"errors"
	"sync"
	"testing"

	"atoman/internal/platform/paperssandbox"

	"github.com/stretchr/testify/require"
)

type fakeExecutor struct {
	mu    sync.Mutex
	run   func(context.Context, Claim) (paperssandbox.Result, error)
	reap  func(context.Context, Claim) error
	calls []string
}

func (e *fakeExecutor) Run(ctx context.Context, c Claim) (paperssandbox.Result, error) {
	e.mu.Lock()
	e.calls = append(e.calls, "run:"+c.SandboxID)
	fn := e.run
	e.mu.Unlock()
	if fn != nil {
		return fn(ctx, c)
	}
	return success(c), nil
}
func (e *fakeExecutor) Reap(ctx context.Context, c Claim) error {
	e.mu.Lock()
	e.calls = append(e.calls, "reap:"+c.SandboxID)
	fn := e.reap
	e.mu.Unlock()
	if fn != nil {
		return fn(ctx, c)
	}
	return nil
}
func TestRunnerPersistsCompletedAndResourceFailures(t *testing.T) {
	q, _, owner, project := setupQueue(t)
	first := submit(t, q, input(owner, project, "manual-pid-fail", PriorityManual, 2))
	second := submit(t, q, input(owner, project, "auto-success", PriorityAuto, 2))
	executor := &fakeExecutor{run: func(_ context.Context, c Claim) (paperssandbox.Result, error) {
		result := success(c)
		if c.Job.ID == first.ID {
			result.Status = paperssandbox.PIDsExceeded
			result.UnderlyingStatus = paperssandbox.PIDsExceeded
			result.ExitCode = 2
			result.ArtifactVerified = false
			return result, errors.New("runsc returned 2")
		}
		return result, nil
	}}
	runner := &Runner{Queue: q, Executor: executor}
	worked, err := runner.Step(context.Background(), "worker-1")
	require.True(t, worked)
	require.ErrorContains(t, err, "runsc returned 2")
	outcome, err := q.Get(context.Background(), owner, first.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, outcome.Status)
	require.Equal(t, "resource_exhausted_pids", outcome.FailureCode)
	worked, err = runner.Step(context.Background(), "worker-2")
	require.NoError(t, err)
	require.True(t, worked)
	outcome, err = q.Get(context.Background(), owner, second.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSucceeded, outcome.Status)
}
func TestRunnerReapsOrphanBeforeNewAttempt(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	original := submit(t, q, input(owner, project, "orphan-retry", PriorityManual, 2))
	old := claim(t, q, "crashed-worker", ClaimRun)
	expireSlot(t, db)
	executor := &fakeExecutor{}
	runner := &Runner{Queue: q, Executor: executor}
	worked, err := runner.Step(context.Background(), "restart-worker")
	require.NoError(t, err)
	require.True(t, worked)
	require.Equal(t, []string{"reap:" + old.SandboxID}, executor.calls)
	job, err := q.Get(context.Background(), owner, original.ID)
	require.NoError(t, err)
	require.Equal(t, StatusQueued, job.Status)
	worked, err = runner.Step(context.Background(), "restart-worker")
	require.NoError(t, err)
	require.True(t, worked)
	job, err = q.Get(context.Background(), owner, original.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSucceeded, job.Status)
	require.Len(t, executor.calls, 2)
	require.NotEqual(t, "run:"+old.SandboxID, executor.calls[1])
}
func TestRunnerUnverifiedReapBlocksSubsequentWork(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	submit(t, q, input(owner, project, "unsafe-reap", PriorityManual, 2))
	claim(t, q, "lost-worker", ClaimRun)
	expireSlot(t, db)
	executor := &fakeExecutor{reap: func(context.Context, Claim) error {
		return errors.New("scope is still active")
	}}
	runner := &Runner{Queue: q, Executor: executor}
	worked, err := runner.Step(context.Background(), "recover-worker")
	require.True(t, worked)
	require.ErrorContains(t, err, "scope is still active")
	worked, err = runner.Step(context.Background(), "next-worker")
	require.False(t, worked)
	require.ErrorIs(t, err, ErrBlocked)
	require.Len(t, executor.calls, 1)
}
func TestRunnerUnverifiedSuccessNeverAccepted(t *testing.T) {
	q, _, owner, project := setupQueue(t)
	job := submit(t, q, input(owner, project, "unverified-success", PriorityManual, 2))
	executor := &fakeExecutor{run: func(_ context.Context, c Claim) (paperssandbox.Result, error) {
		result := success(c)
		result.ArtifactVerified = false
		return result, nil
	}}
	runner := &Runner{Queue: q, Executor: executor}
	worked, err := runner.Step(context.Background(), "worker-1")
	require.NoError(t, err)
	require.True(t, worked)
	stored, err := q.Get(context.Background(), owner, job.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, stored.Status)
	require.Equal(t, "unverified_success_evidence", stored.FailureCode)
}
