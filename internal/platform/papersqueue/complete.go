package papersqueue

import (
	"context"
	"errors"

	"atoman/internal/model"
	"atoman/internal/platform/paperssandbox"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Complete writes a result ONLY after the launcher positively verified
// teardown. The fencing token and epoch prevent an expired worker from
// publishing a PDF or releasing another worker's slot.
func (q *Queue) Complete(ctx context.Context, c Claim, result paperssandbox.Result) error {
	if q == nil || q.db == nil {
		return ErrNotReady
	}
	if c.Kind != ClaimRun || result.JobID != c.SandboxID {
		return ErrInvalid
	}
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, job, now, err := q.validateLease(tx, c, StatusRunning)
		if err != nil {
			return err
		}
		if !result.CleanupVerified || result.Status == paperssandbox.CleanupFailed {
			return blockSlot(tx, job, "execution_cleanup_unverified", now)
		}
		status := StatusFailed
		reason := string(result.Status)
		if result.Status == paperssandbox.Completed {
			if result.ArtifactVerified && result.LimitsVerified && result.SentryObserved &&
				result.ExitKnown && result.ExitCode == 0 &&
				result.UnderlyingStatus == paperssandbox.Completed {
				status = StatusSucceeded
				reason = ""
			} else {
				reason = "unverified_success_evidence"
			}
		} else if result.Status == paperssandbox.Canceled {
			status = StatusCanceled
		} else {
			switch result.Status {
			case paperssandbox.CompileFailed, paperssandbox.MissingArtifact,
				paperssandbox.PIDsExceeded, paperssandbox.MemoryExceeded,
				paperssandbox.ResourceUnknown, paperssandbox.TimedOut,
				paperssandbox.IsolationFailed, paperssandbox.RuntimeFailed:
			default:
				reason = "unknown_result_status"
			}
		}
		accessAllowed, err := eligibleJob(tx, job)
		if err != nil {
			return err
		}
		if !accessAllowed {
			status = StatusCanceled
			reason = "access_revoked"
		} else if job.CancelRequested {
			status = StatusCanceled
			reason = "cancel_requested"
		}
		update := tx.Model(&model.PaperCompileJob{}).
			Where("id = ? AND lease_token = ? AND lease_epoch = ? AND status = ?", job.ID, c.Token, c.Epoch, StatusRunning).
			Updates(map[string]any{
				"status": status, "failure_code": boundedFailureCode(reason),
				"finished_at": now, "worker_id": "", "lease_token": nil, "updated_at": now,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrLeaseLost
		}
		return releaseSlot(tx, c.Epoch, c.Token, now)
	})
}

// RequestCancel is owner-scoped and intentionally never calls runsc. A
// running worker must observe CancelRequested and initiate safe cancellation
// through its supervised execution context; cleanup always precedes Complete.
func (q *Queue) RequestCancel(ctx context.Context, ownerID, jobID uuid.UUID) (model.PaperCompileJob, error) {
	var job model.PaperCompileJob
	if q == nil || q.db == nil {
		return job, ErrNotReady
	}
	if ownerID == uuid.Nil || jobID == uuid.Nil {
		return job, ErrInvalid
	}
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lookup := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND owner_id = ?", jobID, ownerID).Take(&job)
		if errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return ErrNotAuthorized
		}
		if lookup.Error != nil {
			return lookup.Error
		}
		if job.Status == StatusQueued {
			now, err := dbTime(tx)
			if err != nil {
				return err
			}
			if err := tx.Model(&model.PaperCompileJob{}).Where("id = ? AND status = ?", jobID, StatusQueued).
				Updates(map[string]any{
					"status": StatusCanceled, "cancel_requested": true, "failure_code": "owner_canceled",
					"finished_at": now, "updated_at": now,
				}).Error; err != nil {
				return err
			}
			job.Status = StatusCanceled
			job.CancelRequested = true
			job.FinishedAt = &now
		} else if job.Status == StatusRunning || job.Status == StatusRecovering {
			if err := tx.Model(&model.PaperCompileJob{}).Where("id = ?", jobID).
				Update("cancel_requested", true).Error; err != nil {
				return err
			}
			job.CancelRequested = true
		}
		return nil
	})
	return job, err
}

func (q *Queue) Get(ctx context.Context, ownerID, jobID uuid.UUID) (model.PaperCompileJob, error) {
	var job model.PaperCompileJob
	if q == nil || q.db == nil {
		return job, ErrNotReady
	}
	if ownerID == uuid.Nil || jobID == uuid.Nil {
		return job, ErrInvalid
	}
	err := q.db.WithContext(ctx).Where("id = ? AND owner_id = ?", jobID, ownerID).Take(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return job, ErrNotAuthorized
	}
	return job, err
}

// The worker should poll this while executing, not rely on website sessions.
// Stale leases must cease work; cancellation itself is sticky until cleanup.
func (q *Queue) CancellationRequested(ctx context.Context, c Claim) (bool, error) {
	if q == nil || q.db == nil {
		return false, ErrNotReady
	}
	if !c.Valid() {
		return false, ErrInvalid
	}
	var job model.PaperCompileJob
	err := q.db.WithContext(ctx).Where("id = ? AND lease_token = ? AND lease_epoch = ?", c.Job.ID, c.Token, c.Epoch).Take(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, ErrLeaseLost
	}
	if err != nil {
		return false, err
	}
	accessAllowed, err := eligibleJob(q.db.WithContext(ctx), job)
	if err != nil {
		return false, err
	}
	return job.CancelRequested || !accessAllowed, nil
}

// A narrow observability helper for the eventual worker; not a public schema.
func (q *Queue) CountByStatus(ctx context.Context, status string) (int64, error) {
	var count int64
	err := q.db.WithContext(ctx).Model(&model.PaperCompileJob{}).Where("status = ?", status).Count(&count).Error
	return count, err
}
