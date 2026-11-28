package papersqueue

import (
	"atoman/internal/model"
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStrictAdmissionConcurrentOwnerAndIdempotentReplay(t *testing.T) {
	_, db, owner, project := setupQueue(t)
	q, err := NewWithDurablePDF(db, 15*time.Second)
	require.NoError(t, err)
	ctx := context.Background()
	var ok, limited atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, e := q.Submit(ctx, input(owner, project, fmt.Sprintf("parallel-%d", n), PriorityManual, 2))
			if e == nil {
				ok.Add(1)
			} else if e == ErrQuota {
				limited.Add(1)
			} else {
				t.Errorf("unexpected admission error: %v", e)
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 2, ok.Load())
	require.EqualValues(t, 6, limited.Load())
	require.ErrorIs(t, q.CheckAdmission(ctx, owner), ErrQuota)
	var jobs []model.PaperCompileJob
	require.NoError(t, db.Where("owner_id=?", owner).Find(&jobs).Error)
	require.Len(t, jobs, 2)
	repeat, err := q.Submit(ctx, input(owner, project, jobs[0].IdempotencyKey, PriorityManual, 2))
	require.NoError(t, err)
	require.Equal(t, jobs[0].ID, repeat.ID)
	_, err = q.RequestCancel(ctx, owner, jobs[0].ID)
	require.NoError(t, err)
	require.NoError(t, q.CheckAdmission(ctx, owner))
	_, err = q.Submit(ctx, input(owner, project, "after-cancel", PriorityManual, 2))
	require.NoError(t, err)
}
func TestStrictAdmissionGlobalAndHourlyLimits(t *testing.T) {
	_, db, owner, project := setupQueue(t)
	q, err := NewWithDurablePDF(db, 15*time.Second)
	require.NoError(t, err)
	ctx := context.Background()
	for i := 0; i < maxGlobalActive; i++ {
		user := uuid.New()
		p := model.PaperProject{OwnerID: user, Name: "Fixture", HeadRevision: 2}
		require.NoError(t, db.Create(&p).Error)
		require.NoError(t, db.Create(&model.PaperAccessGrant{UserID: user, Status: "active"}).Error)
		submit(t, q, input(user, p.ID, fmt.Sprintf("global-%d", i), PriorityManual, 2))
	}
	_, err = q.Submit(ctx, input(owner, project, "global-limited", PriorityManual, 2))
	require.ErrorIs(t, err, ErrQuota)
	require.NoError(t, db.Model(&model.PaperCompileJob{}).Where("status=?", StatusQueued).Update("status", StatusCanceled).Error)
	for i := 0; i < maxOwnerHourly; i++ {
		j := submit(t, q, input(owner, project, fmt.Sprintf("hourly-%d", i), PriorityManual, 2))
		_, err = q.RequestCancel(ctx, owner, j.ID)
		require.NoError(t, err)
	}
	require.ErrorIs(t, q.CheckAdmission(ctx, owner), ErrQuota)
	_, err = q.Submit(ctx, input(owner, project, "rate-limited", PriorityManual, 2))
	require.ErrorIs(t, err, ErrQuota)
}

func TestReusableCompileRequiresExactPrivateInputsAndDurableResult(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	ctx := context.Background()
	job := submit(t, q, input(owner, project, "manual-existing", PriorityManual, 2))
	find := func(user, pid uuid.UUID, rev int64, compiler, env string, found bool) {
		t.Helper()
		got, ok, err := q.FindReusableCompile(ctx, user, pid, rev, compiler, env)
		require.NoError(t, err)
		require.Equal(t, found, ok)
		if found {
			require.Equal(t, job.ID, got.ID)
		}
	}
	find(owner, project, 2, "pdflatex", environmentDigest, true)
	find(uuid.New(), project, 2, "pdflatex", environmentDigest, false)
	find(owner, uuid.New(), 2, "pdflatex", environmentDigest, false)
	find(owner, project, 3, "pdflatex", environmentDigest, false)
	find(owner, project, 2, "xelatex", environmentDigest, false)
	find(owner, project, 2, "pdflatex", snapshotDigest, false)
	require.NoError(t, db.Model(&model.PaperCompileJob{}).Where("id=?", job.ID).Update("cancel_requested", true).Error)
	find(owner, project, 2, "pdflatex", environmentDigest, false)
	require.NoError(t, db.Model(&model.PaperCompileJob{}).Where("id=?", job.ID).Updates(map[string]any{"status": StatusSucceeded, "cancel_requested": false, "lease_epoch": 1}).Error)
	find(owner, project, 2, "pdflatex", environmentDigest, false)
	require.NoError(t, db.Model(&model.PaperCompileJob{}).Where("id=?", job.ID).Updates(map[string]any{"pdf_size": 1024, "pdf_digest": snapshotDigest}).Error)
	find(owner, project, 2, "pdflatex", environmentDigest, true)
	find(uuid.New(), project, 2, "pdflatex", environmentDigest, false)
	find(owner, uuid.New(), 2, "pdflatex", environmentDigest, false)
}
