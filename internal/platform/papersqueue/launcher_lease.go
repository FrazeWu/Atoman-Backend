package papersqueue

import (
	"context"
	"errors"
	"time"

	"atoman/internal/model"
	"gorm.io/gorm"
)

// ResolveLease is the read-only second-line authorization check for the
// root-owned launcher. It rebuilds the trusted claim from PostgreSQL instead
// of accepting file paths, owner identity or immutable digests sent by a
// less-privileged worker. No caller-supplied values can expand its authority.
func (q *Queue) ResolveLease(ctx context.Context, presented Claim) (Claim, error) {
	if q == nil || q.db == nil {
		return Claim{}, ErrNotReady
	}
	if !presented.Valid() {
		return Claim{}, ErrInvalid
	}
	if presented.Kind != ClaimRun && presented.Kind != ClaimRecover {
		return Claim{}, ErrInvalid
	}
	var verified Claim
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var slot model.PaperCompileSlot
		if err := tx.Where("id = 1").Take(&slot).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotReady
			}
			return err
		}
		now, err := dbTime(tx)
		if err != nil {
			return err
		}
		if slot.Blocked || slot.JobID == nil || *slot.JobID != presented.Job.ID ||
			slot.LeaseToken == nil || *slot.LeaseToken != presented.Token ||
			slot.Epoch != presented.Epoch || slot.WorkerID != presented.WorkerID ||
			slot.LeaseExpiresAt == nil || !now.Before(*slot.LeaseExpiresAt) {
			return ErrLeaseLost
		}
		var job model.PaperCompileJob
		if err := tx.Where("id = ?", *slot.JobID).Take(&job).Error; err != nil {
			return err
		}
		if job.ID != presented.Job.ID || job.SandboxID != presented.SandboxID ||
			job.WorkerID != presented.WorkerID || job.LeaseToken == nil ||
			*job.LeaseToken != presented.Token || job.LeaseEpoch != presented.Epoch {
			return ErrLeaseLost
		}
		required := StatusRunning
		if presented.Kind == ClaimRecover {
			required = StatusRecovering
		}
		if job.Status != required {
			return ErrLeaseLost
		}
		if presented.Kind == ClaimRun {
			valid, err := eligibleJob(tx, job)
			if err != nil {
				return err
			}
			if !valid {
				return ErrNotAuthorized
			}
		}
		verified = Claim{
			Kind: presented.Kind, Job: job, WorkerID: slot.WorkerID,
			Token: *slot.LeaseToken, Epoch: slot.Epoch, SandboxID: job.SandboxID,
			LeaseUntil: *slot.LeaseExpiresAt,
		}
		return nil
	})
	if err != nil {
		return Claim{}, err
	}
	return verified, nil
}

// LeaseMonitor checks against the database's clock rather than trusting a
// worker-side heartbeat claim. It is deliberately never a lease-renewer.
func (q *Queue) LeaseMonitor(ctx context.Context, claim Claim, interval time.Duration, done <-chan struct{}, cancel context.CancelFunc) {
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			probe, stop := context.WithTimeout(context.Background(), interval)
			_, err := q.ResolveLease(probe, claim)
			stop()
			if err != nil {
				cancel()
				return
			}
		}
	}
}
