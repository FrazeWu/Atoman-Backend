package papersqueue

import (
	"context"
	"errors"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (q *Queue) ClaimNext(ctx context.Context, workerID string) (Claim, bool, error) {
	var claim Claim
	if q == nil || q.db == nil {
		return claim, false, ErrNotReady
	}
	if !workerPattern.MatchString(workerID) {
		return claim, false, ErrInvalid
	}
	var found bool
	var blocked bool
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		slot, locked, err := q.lockSlot(tx)
		if err != nil {
			return err
		}
		if !locked {
			return nil
		}
		if slot.Blocked {
			blocked = true
			return nil
		}
		now, err := dbTime(tx)
		if err != nil {
			return err
		}
		if slot.JobID != nil && slot.LeaseExpiresAt != nil && now.Before(*slot.LeaseExpiresAt) {
			return nil
		}
		token := uuid.New()
		epoch := slot.Epoch + 1
		until := now.Add(q.lease)

		if slot.JobID != nil {
			// Never claim a different job while the previous sandbox may still exist.
			var stale model.PaperCompileJob
			row := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", *slot.JobID).Take(&stale)
			if errors.Is(row.Error, gorm.ErrRecordNotFound) {
				// The global slot points to a missing task; keep it blocked for audit.
				blocked = true
				return tx.Model(&model.PaperCompileSlot{}).Where("id=1").
					Updates(map[string]any{"blocked": true, "block_reason": "slot_job_missing", "updated_at": now}).Error
			}
			if row.Error != nil {
				return row.Error
			}
			if stale.SandboxID == "" {
				blocked = true
				return tx.Model(&model.PaperCompileSlot{}).Where("id=1").
					Updates(map[string]any{"blocked": true, "block_reason": "unknown_sandbox_identity", "updated_at": now}).Error
			}
			if err := tx.Model(&model.PaperCompileJob{}).Where("id = ?", stale.ID).Updates(map[string]any{
				"status": StatusRecovering, "worker_id": workerID, "lease_token": token,
				"lease_epoch": epoch, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			stale.Status = StatusRecovering
			stale.WorkerID = workerID
			stale.LeaseToken = &token
			stale.LeaseEpoch = epoch
			claim = Claim{Kind: ClaimRecover, Job: stale, WorkerID: workerID, Token: token, Epoch: epoch, SandboxID: stale.SandboxID, LeaseUntil: until}
		} else {
			// One database-wide slot serializes manual and debounced-auto compiles.
			// SKIP LOCKED protects against concurrent claim scans during enqueue.
			var next model.PaperCompileJob
			row := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
				Where("status = ?", StatusQueued).
				Order("priority DESC, created_at ASC, id ASC").Limit(1).Find(&next)
			if row.Error != nil {
				return row.Error
			}
			if row.RowsAffected == 0 {
				return nil
			}
			accessAllowed, err := eligibleJob(tx, next)
			if err != nil {
				return err
			}
			if next.CancelRequested || !accessAllowed {
				// Revocation/deletion may race with enqueue. Never start stale work.
				reason := "access_revoked"
				if next.CancelRequested {
					reason = "owner_canceled"
				}
				return tx.Model(&model.PaperCompileJob{}).Where("id = ? AND status = ?", next.ID, StatusQueued).
					Updates(map[string]any{
						"status": StatusCanceled, "cancel_requested": true, "failure_code": reason,
						"finished_at": now, "updated_at": now,
					}).Error
			}
			sandboxID := "pc-" + uuid.NewString()[:24]
			if err := tx.Model(&model.PaperCompileJob{}).Where("id = ? AND status = ?", next.ID, StatusQueued).
				Updates(map[string]any{
					"status": StatusRunning, "attempts": next.Attempts + 1, "started_at": now,
					"finished_at": nil, "sandbox_id": sandboxID, "worker_id": workerID,
					"lease_token": token, "lease_epoch": epoch, "updated_at": now,
				}).Error; err != nil {
				return err
			}
			next.Attempts++
			next.Status = StatusRunning
			next.SandboxID = sandboxID
			next.WorkerID = workerID
			next.LeaseToken = &token
			next.LeaseEpoch = epoch
			next.StartedAt = &now
			claim = Claim{Kind: ClaimRun, Job: next, WorkerID: workerID, Token: token, Epoch: epoch, SandboxID: sandboxID, LeaseUntil: until}
		}
		if err := tx.Model(&model.PaperCompileSlot{}).Where("id=1").Updates(map[string]any{
			"job_id": claim.Job.ID, "worker_id": workerID, "lease_token": token,
			"epoch": epoch, "lease_expires_at": until, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		return Claim{}, false, err
	}
	if blocked {
		return Claim{}, false, ErrBlocked
	}
	return claim, found, nil
}

func (q *Queue) validateLease(tx *gorm.DB, c Claim, requireStatus string) (model.PaperCompileSlot, model.PaperCompileJob, time.Time, error) {
	var slot model.PaperCompileSlot
	var job model.PaperCompileJob
	if !c.Valid() {
		return slot, job, time.Time{}, ErrInvalid
	}
	locked, exists, err := q.lockSlot(tx)
	if err != nil {
		return slot, job, time.Time{}, err
	}
	if !exists {
		return slot, job, time.Time{}, ErrLeaseLost
	}
	slot = locked
	now, err := dbTime(tx)
	if err != nil {
		return slot, job, now, err
	}
	if slot.Blocked || slot.JobID == nil || *slot.JobID != c.Job.ID ||
		slot.LeaseToken == nil || *slot.LeaseToken != c.Token ||
		slot.Epoch != c.Epoch || slot.WorkerID != c.WorkerID ||
		slot.LeaseExpiresAt == nil || !now.Before(*slot.LeaseExpiresAt) {
		return slot, job, now, ErrLeaseLost
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", c.Job.ID).Take(&job).Error; err != nil {
		return slot, job, now, err
	}
	if job.Status != requireStatus || job.LeaseToken == nil || *job.LeaseToken != c.Token ||
		job.LeaseEpoch != c.Epoch || job.WorkerID != c.WorkerID ||
		job.SandboxID != c.SandboxID {
		return slot, job, now, ErrLeaseLost
	}
	return slot, job, now, nil
}

// Heartbeat extends the lease only for the current fenced owner. Time comes
// from PostgreSQL to avoid divergence across worker machines.
func (q *Queue) Heartbeat(ctx context.Context, c Claim) error {
	if q == nil || q.db == nil {
		return ErrNotReady
	}
	state := StatusRunning
	if c.Kind == ClaimRecover {
		state = StatusRecovering
	}
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, job, now, err := q.validateLease(tx, c, state)
		if err != nil {
			return err
		}
		allowed, err := eligibleJob(tx, job)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrNotAuthorized
		}
		res := tx.Model(&model.PaperCompileSlot{}).Where("id = 1 AND epoch = ? AND lease_token = ?", c.Epoch, c.Token).
			Updates(map[string]any{"lease_expires_at": now.Add(q.lease), "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrLeaseLost
		}
		return nil
	})
}

func releaseSlot(tx *gorm.DB, epoch int64, token uuid.UUID, now time.Time) error {
	res := tx.Model(&model.PaperCompileSlot{}).Where("id = 1 AND epoch = ? AND lease_token = ?", epoch, token).
		Updates(map[string]any{
			"job_id": nil, "worker_id": "", "lease_token": nil, "lease_expires_at": nil, "updated_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrLeaseLost
	}
	return nil
}

// blockSlot persists the safety stop across process restarts. Only a
// separately audited operator recovery flow may unblock this row.
func blockSlot(tx *gorm.DB, job model.PaperCompileJob, reason string, now time.Time) error {
	if err := tx.Model(&model.PaperCompileJob{}).Where("id = ?", job.ID).
		Updates(map[string]any{
			"status": StatusBlocked, "failure_code": boundedFailureCode(reason), "updated_at": now,
		}).Error; err != nil {
		return err
	}
	return tx.Model(&model.PaperCompileSlot{}).Where("id = 1").
		Updates(map[string]any{"blocked": true, "block_reason": boundedFailureCode(reason), "updated_at": now}).Error
}

// FinishRecovery must run only after the previous sandbox was positively
// cleaned using its *old* SandboxID. A stale worker cannot acknowledge.
func (q *Queue) FinishRecovery(ctx context.Context, c Claim, cleanupVerified bool) error {
	if q == nil || q.db == nil {
		return ErrNotReady
	}
	if c.Kind != ClaimRecover {
		return ErrInvalid
	}
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, job, now, err := q.validateLease(tx, c, StatusRecovering)
		if err != nil {
			return err
		}
		if !cleanupVerified {
			return blockSlot(tx, job, "recovery_cleanup_unverified", now)
		}
		status := StatusQueued
		failure := "previous_worker_lost"
		var finishedAt any = nil
		accessAllowed, err := eligibleJob(tx, job)
		if err != nil {
			return err
		}
		if !accessAllowed {
			status = StatusCanceled
			failure = "access_revoked_during_recovery"
			finishedAt = now
		} else if job.CancelRequested {
			status = StatusCanceled
			failure = "canceled_during_recovery"
			finishedAt = now
		} else if job.Attempts >= job.MaxAttempts {
			status = StatusFailed
			failure = "attempt_limit_after_recovery"
			finishedAt = now
		}
		if err := tx.Model(&model.PaperCompileJob{}).Where("id = ? AND lease_epoch = ? AND lease_token = ?", job.ID, c.Epoch, c.Token).
			Updates(map[string]any{
				"status": status, "failure_code": failure,
				"worker_id": "", "lease_token": nil, "sandbox_id": "",
				"finished_at": finishedAt, "updated_at": now,
			}).Error; err != nil {
			return err
		}
		return releaseSlot(tx, c.Epoch, c.Token, now)
	})
}
