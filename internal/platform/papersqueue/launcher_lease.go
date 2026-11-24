package papersqueue

import (
	"context"
	"time"

	"atoman/internal/model"
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
	// One statement uses a consistent PostgreSQL snapshot for the slot,
	// job and access grant. Avoid four sequential DB round trips during
	// each two-second lease verification while keeping every fence intact.
	required := StatusRunning
	if presented.Kind == ClaimRecover {
		required = StatusRecovering
	}
	var row struct {
		model.PaperCompileJob
		VerifiedUntil time.Time
		Allowed       bool
	}
	query := q.db.WithContext(ctx).Raw(`
 SELECT j.*, s.lease_expires_at AS verified_until,
 EXISTS (
   SELECT 1 FROM paper_projects p
   JOIN paper_access_grants g ON g.user_id=p.owner_id
   WHERE p.id=j.project_id AND p.owner_id=j.owner_id
     AND p.deleted_at IS NULL AND g.status='active'
     AND (g.expires_at IS NULL OR g.expires_at > statement_timestamp())
 ) AS allowed
 FROM paper_compile_slots s
 JOIN paper_compile_jobs j ON j.id=s.job_id
 WHERE s.id=1 AND NOT s.blocked
   AND s.job_id=? AND s.lease_token=? AND s.epoch=? AND s.worker_id=?
   AND s.lease_expires_at > statement_timestamp()
   AND j.sandbox_id=? AND j.worker_id=s.worker_id
   AND j.lease_token=s.lease_token AND j.lease_epoch=s.epoch AND j.status=?
 `, presented.Job.ID, presented.Token, presented.Epoch, presented.WorkerID, presented.SandboxID, required).Scan(&row)
	if query.Error != nil {
		return Claim{}, query.Error
	}
	if query.RowsAffected != 1 || row.ID != presented.Job.ID || row.VerifiedUntil.IsZero() {
		return Claim{}, ErrLeaseLost
	}
	if presented.Kind == ClaimRun && !row.Allowed {
		return Claim{}, ErrNotAuthorized
	}
	return Claim{Kind: presented.Kind, Job: row.PaperCompileJob, WorkerID: row.WorkerID,
		Token: presented.Token, Epoch: presented.Epoch, SandboxID: row.SandboxID, LeaseUntil: row.VerifiedUntil}, nil
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
