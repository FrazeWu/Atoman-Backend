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
