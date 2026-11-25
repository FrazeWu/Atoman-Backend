package papersqueue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/paperssandbox"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const snapshotDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const environmentDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func setupQueue(t *testing.T) (*Queue, *gorm.DB, uuid.UUID, uuid.UUID) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.PaperAccessGrant{}, &model.PaperProject{}, &model.PaperCompileJob{}, &model.PaperCompileSlot{})
	require.NoError(t, db.Create(&model.PaperCompileSlot{ID: 1}).Error)
	owner := uuid.New()
	project := model.PaperProject{OwnerID: owner, Name: "Test paper", HeadRevision: 2}
	require.NoError(t, db.Create(&project).Error)
	require.NoError(t, db.Create(&model.PaperAccessGrant{UserID: owner, Status: "active"}).Error)
	queue, err := New(db, 15*time.Second)
	require.NoError(t, err)
	return queue, db, owner, project.ID
}
func input(owner, project uuid.UUID, key string, priority, max int) SubmitInput {
	return SubmitInput{
		OwnerID: owner, ProjectID: project, SourceRevision: 2,
		SnapshotDigest: snapshotDigest, EnvironmentDigest: environmentDigest,
		MainFile: "main.tex", IdempotencyKey: key, Priority: priority, MaxAttempts: max,
	}
}
func submit(t *testing.T, q *Queue, i SubmitInput) model.PaperCompileJob {
	t.Helper()
	j, err := q.Submit(context.Background(), i)
	require.NoError(t, err)
	return j
}
func claim(t *testing.T, q *Queue, worker string, kind string) Claim {
	t.Helper()
	c, ok, err := q.ClaimNext(context.Background(), worker)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, kind, c.Kind)
	require.True(t, c.Valid())
	return c
}
func success(c Claim) paperssandbox.Result {
	return paperssandbox.Result{
		JobID: c.SandboxID, Status: paperssandbox.Completed,
		UnderlyingStatus: paperssandbox.Completed, ExitCode: 0, ExitKnown: true,
		SentryObserved: true, LimitsVerified: true, ArtifactVerified: true, CleanupVerified: true,
	}
}
func expireSlot(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec("UPDATE paper_compile_slots SET lease_expires_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id=1").Error)
}
func TestPersistentCompileQueueIdempotencyPriorityAndAuth(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	ctx := context.Background()
	auto := submit(t, q, input(owner, project, "autosave-1", PriorityAuto, 2))
	same, err := q.Submit(ctx, input(owner, project, "autosave-1", PriorityAuto, 2))
	require.NoError(t, err)
	require.Equal(t, auto.ID, same.ID)
	var count int64
	require.NoError(t, db.Model(&model.PaperCompileJob{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	mismatch := input(owner, project, "autosave-1", PriorityManual, 2)
	_, err = q.Submit(ctx, mismatch)
	require.ErrorIs(t, err, ErrIdempotency)
	_, err = q.Submit(ctx, input(uuid.New(), project, "outsider", PriorityManual, 2))
	require.ErrorIs(t, err, ErrNotAuthorized)
	bad := input(owner, project, "stale", PriorityManual, 2)
	bad.SourceRevision = 1
	_, err = q.Submit(ctx, bad)
	require.ErrorIs(t, err, ErrInvalid)
	bad = input(owner, project, "bad-digest", PriorityManual, 2)
	bad.SnapshotDigest = "sha256:NOT_REAL"
	_, err = q.Submit(ctx, bad)
	require.ErrorIs(t, err, ErrInvalid)

	manual := submit(t, q, input(owner, project, "manual-1", PriorityManual, 2))
	first := claim(t, q, "worker-1", ClaimRun)
	require.Equal(t, manual.ID, first.Job.ID)
	require.Regexp(t, `^pc-[a-f0-9]{24}$`, first.SandboxID)
	require.EqualValues(t, 1, first.Job.Attempts)
	q2, err := New(db, 15*time.Second)
	require.NoError(t, err)
	_, ok, err := q2.ClaimNext(ctx, "worker-2")
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, q.Complete(ctx, first, success(first)))
	second := claim(t, q2, "worker-2", ClaimRun)
	require.Equal(t, auto.ID, second.Job.ID)
	require.NoError(t, q2.Complete(ctx, second, success(second)))
	_, ok, err = q.ClaimNext(ctx, "worker-1")
	require.NoError(t, err)
	require.False(t, ok)
	for _, id := range []uuid.UUID{auto.ID, manual.ID} {
		j, err := q.Get(ctx, owner, id)
		require.NoError(t, err)
		require.Equal(t, StatusSucceeded, j.Status)
	}
	_, err = q.Get(ctx, uuid.New(), auto.ID)
	require.ErrorIs(t, err, ErrNotAuthorized)
}

func TestLeaseExpiryRecoveryFencesOldWorkerAndRetries(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	job := submit(t, q, input(owner, project, "job-recover", PriorityManual, 2))
	old := claim(t, q, "worker-a", ClaimRun)
	require.Equal(t, job.ID, old.Job.ID)
	expireSlot(t, db)
	recovered := claim(t, q, "worker-b", ClaimRecover)
	require.Equal(t, old.SandboxID, recovered.SandboxID)
	require.Greater(t, recovered.Epoch, old.Epoch)
	require.NotEqual(t, old.Token, recovered.Token)
	require.ErrorIs(t, q.Heartbeat(context.Background(), old), ErrLeaseLost)
	require.ErrorIs(t, q.Complete(context.Background(), old, success(old)), ErrLeaseLost)
	_, ok, err := q.ClaimNext(context.Background(), "worker-c")
	require.NoError(t, err)
	require.False(t, ok, "recovery must block new jobs")
	require.NoError(t, q.FinishRecovery(context.Background(), recovered, true))
	again := claim(t, q, "worker-c", ClaimRun)
	require.Equal(t, job.ID, again.Job.ID)
	require.NotEqual(t, old.SandboxID, again.SandboxID)
	require.Equal(t, 2, again.Job.Attempts)
	require.NoError(t, q.Complete(context.Background(), again, success(again)))
	require.ErrorIs(t, q.Complete(context.Background(), old, success(old)), ErrLeaseLost)
}

func TestRecoveryQuarantinesUnverifiedCleanupAcrossProcessRestart(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	submit(t, q, input(owner, project, "blocked-job", PriorityManual, 2))
	first := claim(t, q, "worker-a", ClaimRun)
	expireSlot(t, db)
	recovery := claim(t, q, "worker-b", ClaimRecover)
	require.ErrorIs(t, q.Complete(context.Background(), first, success(first)), ErrLeaseLost)
	require.NoError(t, q.FinishRecovery(context.Background(), recovery, false))
	restarted, err := New(db, 15*time.Second)
	require.NoError(t, err)
	_, ok, err := restarted.ClaimNext(context.Background(), "worker-c")
	require.False(t, ok)
	require.ErrorIs(t, err, ErrBlocked)
	slot, err := restarted.SlotState(context.Background())
	require.NoError(t, err)
	require.True(t, slot.Blocked)
	require.Equal(t, "recovery_cleanup_unverified", slot.BlockReason)
}
func TestLeaseLossAfterHeartbeatExpiryAndAttemptExhaustion(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	submit(t, q, input(owner, project, "once-only", PriorityManual, 1))
	first := claim(t, q, "worker-a", ClaimRun)
	require.NoError(t, q.Heartbeat(context.Background(), first))
	expireSlot(t, db)
	recovery := claim(t, q, "worker-b", ClaimRecover)
	require.NoError(t, q.FinishRecovery(context.Background(), recovery, true))
	j, err := q.Get(context.Background(), owner, first.Job.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, j.Status)
	require.Equal(t, "attempt_limit_after_recovery", j.FailureCode)
	_, ok, err := q.ClaimNext(context.Background(), "worker-c")
	require.NoError(t, err)
	require.False(t, ok)
}
func TestCompileCleanupFailureBlocksNewWork(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	submit(t, q, input(owner, project, "cleanup-unsafe", PriorityManual, 2))
	first := claim(t, q, "worker-a", ClaimRun)
	outcome := success(first)
	outcome.CleanupVerified = false
	outcome.Status = paperssandbox.CleanupFailed
	require.NoError(t, q.Complete(context.Background(), first, outcome))
	another, err := New(db, 15*time.Second)
	require.NoError(t, err)
	_, ok, err := another.ClaimNext(context.Background(), "worker-b")
	require.False(t, ok)
	require.ErrorIs(t, err, ErrBlocked)
}
func TestOwnerCancelQueuedAndRunning(t *testing.T) {
	q, _, owner, project := setupQueue(t)
	queued := submit(t, q, input(owner, project, "cancel-before-run", PriorityManual, 2))
	_, err := q.RequestCancel(context.Background(), uuid.New(), queued.ID)
	require.ErrorIs(t, err, ErrNotAuthorized)
	got, err := q.RequestCancel(context.Background(), owner, queued.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, got.Status)
	running := submit(t, q, input(owner, project, "cancel-in-flight", PriorityManual, 2))
	c := claim(t, q, "worker-a", ClaimRun)
	require.Equal(t, running.ID, c.Job.ID)
	_, err = q.RequestCancel(context.Background(), owner, running.ID)
	require.NoError(t, err)
	canceled, err := q.CancellationRequested(context.Background(), c)
	require.NoError(t, err)
	require.True(t, canceled)
	require.NoError(t, q.Complete(context.Background(), c, success(c)))
	current, err := q.Get(context.Background(), owner, running.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, current.Status)
}
func TestManyProcessesCannotClaimSameGlobalSlot(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	for i := 0; i < 4; i++ {
		submit(t, q, input(owner, project, fmt.Sprintf("concurrent-%02d", i), PriorityAuto, 2))
	}
	var successCount atomic.Int64
	var wg sync.WaitGroup
	errorsCh := make(chan error, 20)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			peer, _ := New(db, 15*time.Second)
			_, ok, err := peer.ClaimNext(context.Background(), fmt.Sprintf("worker-%d", idx))
			if err != nil {
				errorsCh <- err
				return
			}
			if ok {
				successCount.Add(1)
			}
		}(i)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, successCount.Load(), "only one worker may reserve global slot")
}
func TestQueuedJobsSurviveFreshQueueObjectAndLeaseRecovery(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	submit(t, q, input(owner, project, "persisted", PriorityAuto, 2))
	restarted, err := New(db, 15*time.Second)
	require.NoError(t, err)
	c := claim(t, restarted, "fresh-worker", ClaimRun)
	require.NoError(t, restarted.Complete(context.Background(), c, success(c)))
}
func TestMissingSeedIsNotAValidProductionQueue(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.PaperCompileSlot{}, &model.PaperCompileJob{})
	q, err := New(db, 15*time.Second)
	require.NoError(t, err)
	_, err = q.SlotState(context.Background())
	require.ErrorIs(t, err, ErrNotReady)
	_, claimed, err := q.ClaimNext(context.Background(), "worker-1")
	require.ErrorIs(t, err, ErrNotReady)
	require.False(t, claimed)
}

func TestConcurrentIdempotentSubmitCreatesOnlyOneJob(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	input := input(owner, project, "same-request-token", PriorityAuto, 2)
	const parallel = 8
	var wg sync.WaitGroup
	ids := make(chan uuid.UUID, parallel)
	errs := make(chan error, parallel)
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := q.Submit(context.Background(), input)
			if err != nil {
				errs <- err
				return
			}
			ids <- job.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	observed := map[uuid.UUID]bool{}
	for id := range ids {
		observed[id] = true
	}
	require.Len(t, observed, 1)
	var count int64
	require.NoError(t, db.Model(&model.PaperCompileJob{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestRecoveryFencingAlsoProtectsPreviousRecoverer(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	submit(t, q, input(owner, project, "repeat-recovery", PriorityManual, 3))
	original := claim(t, q, "worker-1", ClaimRun)
	expireSlot(t, db)
	firstRecoverer := claim(t, q, "reaper-1", ClaimRecover)
	expireSlot(t, db)
	secondRecoverer := claim(t, q, "reaper-2", ClaimRecover)
	require.Greater(t, secondRecoverer.Epoch, firstRecoverer.Epoch)
	require.NotEqual(t, firstRecoverer.Token, secondRecoverer.Token)
	require.Equal(t, original.SandboxID, secondRecoverer.SandboxID)
	require.ErrorIs(t, q.FinishRecovery(context.Background(), firstRecoverer, true), ErrLeaseLost)
	require.NoError(t, q.FinishRecovery(context.Background(), secondRecoverer, true))
	newRun := claim(t, q, "worker-2", ClaimRun)
	require.Greater(t, newRun.Epoch, secondRecoverer.Epoch)
	require.NoError(t, q.Complete(context.Background(), newRun, success(newRun)))
}

func TestCancelRequestedWhileRecoveringStaysCanceledAfterCleanup(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	submitted := submit(t, q, input(owner, project, "cancel-recover", PriorityManual, 2))
	claim(t, q, "failed-worker", ClaimRun)
	expireSlot(t, db)
	recovering := claim(t, q, "new-worker", ClaimRecover)
	_, err := q.RequestCancel(context.Background(), owner, submitted.ID)
	require.NoError(t, err)
	require.NoError(t, q.FinishRecovery(context.Background(), recovering, true))
	stored, err := q.Get(context.Background(), owner, submitted.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, stored.Status)
	_, claimed, err := q.ClaimNext(context.Background(), "worker-third")
	require.NoError(t, err)
	require.False(t, claimed)
}

func TestIdempotentRetrySurvivesLaterProjectEdits(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	request := input(owner, project, "retry-old-revision", PriorityAuto, 2)
	first := submit(t, q, request)
	require.NoError(t, db.Model(&model.PaperProject{}).Where("id = ?", project).Update("head_revision", 3).Error)
	repeated, err := q.Submit(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, first.ID, repeated.ID)
	newKey := request
	newKey.IdempotencyKey = "new-job-with-old-revision"
	_, err = q.Submit(context.Background(), newKey)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestRevokedInvitePreventsQueuedRunAndNewSubmit(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	pending := submit(t, q, input(owner, project, "pending-before-revoke", PriorityManual, 2))
	require.NoError(t, db.Model(&model.PaperAccessGrant{}).
		Where("user_id = ?", owner).Update("status", "revoked").Error)
	_, claimed, err := q.ClaimNext(context.Background(), "worker-1")
	require.NoError(t, err)
	require.False(t, claimed)
	job, err := q.Get(context.Background(), owner, pending.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, job.Status)
	require.Equal(t, "access_revoked", job.FailureCode)
	_, err = q.Submit(context.Background(), input(owner, project, "new-after-revoke", PriorityManual, 2))
	require.ErrorIs(t, err, ErrNotAuthorized)
}

func TestInviteRevokedWhileRunningForbidsHeartbeatAndSuccess(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	submit(t, q, input(owner, project, "revoked-in-flight", PriorityManual, 2))
	running := claim(t, q, "worker-1", ClaimRun)
	require.NoError(t, db.Model(&model.PaperAccessGrant{}).
		Where("user_id = ?", owner).Update("status", "revoked").Error)
	require.ErrorIs(t, q.Heartbeat(context.Background(), running), ErrNotAuthorized)
	cancel, err := q.CancellationRequested(context.Background(), running)
	require.NoError(t, err)
	require.True(t, cancel)
	require.NoError(t, q.Complete(context.Background(), running, success(running)))
	job, err := q.Get(context.Background(), owner, running.Job.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, job.Status)
	require.Equal(t, "access_revoked", job.FailureCode)
}

func TestRevokedInviteDuringRecoveryCannotReschedule(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	submit(t, q, input(owner, project, "revoked-during-recovery", PriorityManual, 2))
	running := claim(t, q, "worker-1", ClaimRun)
	expireSlot(t, db)
	recovery := claim(t, q, "worker-2", ClaimRecover)
	require.Equal(t, running.SandboxID, recovery.SandboxID)
	require.NoError(t, db.Model(&model.PaperAccessGrant{}).
		Where("user_id = ?", owner).Update("status", "revoked").Error)
	require.NoError(t, q.FinishRecovery(context.Background(), recovery, true))
	job, err := q.Get(context.Background(), owner, running.Job.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, job.Status)
	_, claimed, err := q.ClaimNext(context.Background(), "worker-3")
	require.NoError(t, err)
	require.False(t, claimed)
}

func TestQueueIdempotencyIncludesCompiler(t *testing.T) {
	q, _, owner, project := setupQueue(t)
	ctx := context.Background()
	i := input(owner, project, "engine-key", PriorityManual, 2)
	job := submit(t, q, i)
	require.Equal(t, "pdflatex", job.Compiler)
	i.Compiler = "lualatex"
	_, err := q.Submit(ctx, i)
	require.ErrorIs(t, err, ErrIdempotency)
	i.IdempotencyKey = "lua-key"
	job = submit(t, q, i)
	require.Equal(t, "lualatex", job.Compiler)
	i.IdempotencyKey = "unapproved-key"
	i.Compiler = "xelatex"
	_, err = q.Submit(ctx, i)
	require.ErrorIs(t, err, ErrInvalid)
}
