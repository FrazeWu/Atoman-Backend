package papersqueue

import (
	"context"
	"errors"
	"testing"

	"atoman/internal/platform/paperssandbox"
	"github.com/stretchr/testify/require"
)

func TestLauncherTransportFailureLeavesLeaseRecoverable(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	original := submit(t, q, input(owner, project, "crashed-launcher", PriorityManual, 2))
	runner := &Runner{Queue: q, Executor: &fakeExecutor{
		run: func(_ context.Context, c Claim) (paperssandbox.Result, error) {
			return paperssandbox.Result{JobID: c.SandboxID}, ErrExecutionUncertain
		},
	}}
	claimed, err := runner.Step(context.Background(), "worker-launcher-crash")
	require.True(t, claimed)
	require.ErrorIs(t, err, ErrExecutionUncertain)
	before, err := q.Get(context.Background(), owner, original.ID)
	require.NoError(t, err)
	require.Equal(t, StatusRunning, before.Status)
	require.EqualValues(t, 1, before.Attempts)
	require.NotEmpty(t, before.SandboxID)
	slot, err := q.SlotState(context.Background())
	require.NoError(t, err)
	require.False(t, slot.Blocked)
	require.NotNil(t, slot.JobID)

	// The old slot is not released; expiry is mandatory before Reap.
	another, ok, err := q.ClaimNext(context.Background(), "unsafe-next-worker")
	require.NoError(t, err)
	require.False(t, ok)
	require.False(t, another.Valid())
	expireSlot(t, db)
	recovered := claim(t, q, "new-launcher-reaper", ClaimRecover)
	require.Equal(t, before.SandboxID, recovered.SandboxID)
	require.NoError(t, q.FinishRecovery(context.Background(), recovered, true))
	fresh := claim(t, q, "fresh-worker", ClaimRun)
	require.NotEqual(t, recovered.SandboxID, fresh.SandboxID)
	require.NoError(t, q.Complete(context.Background(), fresh, success(fresh)))
	stored, err := q.Get(context.Background(), owner, original.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSucceeded, stored.Status)
	require.EqualValues(t, 2, stored.Attempts)
}

func TestLauncherTransportFailureDuringRecoveryDoesNotAttestCleanup(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	original := submit(t, q, input(owner, project, "recovery-launcher-crash", PriorityManual, 3))
	first := claim(t, q, "lost-before-recovery", ClaimRun)
	expireSlot(t, db)
	runner := &Runner{Queue: q, Executor: &fakeExecutor{reap: func(context.Context, Claim) error {
		return errors.Join(ErrExecutionUncertain, errors.New("launcher terminated during cleanup"))
	}}}
	claimed, err := runner.Step(context.Background(), "reaper-launcher-crash")
	require.True(t, claimed)
	require.ErrorIs(t, err, ErrExecutionUncertain)
	job, err := q.Get(context.Background(), owner, original.ID)
	require.NoError(t, err)
	require.Equal(t, StatusRecovering, job.Status)
	slot, err := q.SlotState(context.Background())
	require.NoError(t, err)
	require.False(t, slot.Blocked)
	require.EqualValues(t, 2, slot.Epoch)
	_, ok, err := q.ClaimNext(context.Background(), "other-worker")
	require.NoError(t, err)
	require.False(t, ok)
	expireSlot(t, db)
	retryReap := claim(t, q, "reaper-restarted", ClaimRecover)
	require.Equal(t, first.SandboxID, retryReap.SandboxID)
	require.EqualValues(t, 3, retryReap.Epoch)
	require.NoError(t, q.FinishRecovery(context.Background(), retryReap, true))
	queued, err := q.Get(context.Background(), owner, original.ID)
	require.NoError(t, err)
	require.Equal(t, StatusQueued, queued.Status)
}

func TestUnattestedCleanupStillQuarantinesImmediately(t *testing.T) {
	q, _, owner, project := setupQueue(t)
	original := submit(t, q, input(owner, project, "unverified-cleanup", PriorityManual, 2))
	runner := &Runner{Queue: q, Executor: &fakeExecutor{run: func(_ context.Context, c Claim) (paperssandbox.Result, error) {
		return paperssandbox.Result{JobID: c.SandboxID, Status: paperssandbox.CleanupFailed,
			CleanupVerified: false}, errors.New("cleanup could not be verified")
	}}}
	claimed, err := runner.Step(context.Background(), "worker-fails-cleanup")
	require.True(t, claimed)
	require.ErrorContains(t, err, "cleanup could not be verified")
	job, err := q.Get(context.Background(), owner, original.ID)
	require.NoError(t, err)
	require.Equal(t, StatusBlocked, job.Status)
	_, ok, err := q.ClaimNext(context.Background(), "another-worker")
	require.False(t, ok)
	require.ErrorIs(t, err, ErrBlocked)
}
