package papersqueue

import (
	"context"
	"testing"

	"atoman/internal/model"
	"github.com/stretchr/testify/require"
)

func TestLauncherLeaseRebuildsInputsAndFencesOldClaims(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	job := submit(t, q, input(owner, project, "signed-scope", PriorityManual, 2))
	active := claim(t, q, "worker-a", ClaimRun)
	require.Equal(t, job.ID, active.Job.ID)

	// The socket caller does not control immutable file or environment values.
	falsified := active
	falsified.Job.SnapshotDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	falsified.Job.EnvironmentDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	falsified.Job.ProjectID = owner
	resolved, err := q.ResolveLease(context.Background(), falsified)
	require.NoError(t, err)
	require.Equal(t, job.SnapshotDigest, resolved.Job.SnapshotDigest)
	require.Equal(t, job.EnvironmentDigest, resolved.Job.EnvironmentDigest)
	require.Equal(t, project, resolved.Job.ProjectID)

	wrongToken := active
	wrongToken.Token = job.ID
	_, err = q.ResolveLease(context.Background(), wrongToken)
	require.ErrorIs(t, err, ErrLeaseLost)
	expireSlot(t, db)
	_, err = q.ResolveLease(context.Background(), active)
	require.ErrorIs(t, err, ErrLeaseLost)
	reaper := claim(t, q, "worker-b", ClaimRecover)
	_, err = q.ResolveLease(context.Background(), active)
	require.ErrorIs(t, err, ErrLeaseLost)
	authorized, err := q.ResolveLease(context.Background(), reaper)
	require.NoError(t, err)
	require.Equal(t, ClaimRecover, authorized.Kind)
	require.NoError(t, q.FinishRecovery(context.Background(), reaper, true))
	_, err = q.ResolveLease(context.Background(), reaper)
	require.ErrorIs(t, err, ErrLeaseLost)
}
func TestLauncherNeverRunsRevokedProjectAndStillAllowsSafeReap(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	submit(t, q, input(owner, project, "revoke", PriorityAuto, 2))
	running := claim(t, q, "worker-revoked", ClaimRun)
	require.NoError(t, db.Model(&model.PaperAccessGrant{}).Where("user_id = ?", owner).Update("status", "revoked").Error)
	_, err := q.ResolveLease(context.Background(), running)
	require.ErrorIs(t, err, ErrNotAuthorized)
	expireSlot(t, db)
	recovery := claim(t, q, "worker-reaper", ClaimRecover)
	_, err = q.ResolveLease(context.Background(), recovery)
	require.NoError(t, err, "expired or revoked access must not prevent cleanup")
}
