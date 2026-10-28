// Package papersqueue implements a PostgreSQL-backed, fail-closed compile
// queue. No public handler or production worker uses this P0 implementation yet.
package papersqueue

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StatusQueued     = "queued"
	StatusRunning    = "running"
	StatusRecovering = "recovering"
	StatusSucceeded  = "succeeded"
	StatusFailed     = "failed"
	StatusCanceled   = "canceled"
	StatusBlocked    = "blocked"

	ClaimRun       = "run"
	ClaimRecover   = "recover"
	PriorityManual = 10
	PriorityAuto   = 0
)

var (
	ErrInvalid         = errors.New("papers queue: invalid request")
	ErrNotReady        = errors.New("papers queue: schema or global slot not initialized")
	ErrLeaseLost       = errors.New("papers queue: lease expired, superseded or fenced")
	ErrBlocked         = errors.New("papers queue: global compile slot quarantined")
	ErrIdempotency     = errors.New("papers queue: idempotency key reused for different input")
	ErrNotAuthorized   = errors.New("papers queue: owner invitation or project not available")
	digestPattern      = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	idempotencyPattern = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,96}$`)
	workerPattern      = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,64}$`)
)

type Queue struct {
	db    *gorm.DB
	lease time.Duration
}

func New(db *gorm.DB, lease time.Duration) (*Queue, error) {
	if db == nil || db.Dialector.Name() != "postgres" || lease < 10*time.Second || lease > 5*time.Minute {
		return nil, ErrInvalid
	}
	return &Queue{db: db, lease: lease}, nil
}

type SubmitInput struct {
	OwnerID           uuid.UUID
	ProjectID         uuid.UUID
	SourceRevision    int64
	SnapshotDigest    string
	EnvironmentDigest string
	MainFile          string
	IdempotencyKey    string
	Priority          int
	MaxAttempts       int
}

func (i SubmitInput) validate() error {
	if i.OwnerID == uuid.Nil || i.ProjectID == uuid.Nil || i.SourceRevision <= 0 ||
		!digestPattern.MatchString(i.SnapshotDigest) || !digestPattern.MatchString(i.EnvironmentDigest) ||
		i.MainFile != "main.tex" || !idempotencyPattern.MatchString(i.IdempotencyKey) ||
		(i.Priority != PriorityManual && i.Priority != PriorityAuto) ||
		i.MaxAttempts < 1 || i.MaxAttempts > 3 {
		return ErrInvalid
	}
	return nil
}
func matching(existing model.PaperCompileJob, i SubmitInput) bool {
	return existing.OwnerID == i.OwnerID && existing.ProjectID == i.ProjectID &&
		existing.SourceRevision == i.SourceRevision &&
		existing.SnapshotDigest == i.SnapshotDigest &&
		existing.EnvironmentDigest == i.EnvironmentDigest &&
		existing.MainFile == i.MainFile && existing.IdempotencyKey == i.IdempotencyKey &&
		existing.Priority == i.Priority && existing.MaxAttempts == i.MaxAttempts
}

// Submit is for an internal, trusted snapshot publisher only. A future
// public-facing handler MUST authenticate owner and verify the immutable
// snapshot/environment blobs before calling this method.
func (q *Queue) Submit(ctx context.Context, i SubmitInput) (model.PaperCompileJob, error) {
	var job model.PaperCompileJob
	if q == nil || q.db == nil {
		return job, ErrNotReady
	}
	if err := i.validate(); err != nil {
		return job, err
	}
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var seeded int64
		if err := tx.Model(&model.PaperCompileSlot{}).Where("id = 1").Count(&seeded).Error; err != nil {
			return err
		}
		if seeded != 1 {
			return ErrNotReady
		}
		var project model.PaperProject
		lookup := tx.Clauses(clause.Locking{Strength: "SHARE"}).
			Where("id = ? AND owner_id = ? AND deleted_at IS NULL", i.ProjectID, i.OwnerID).First(&project)
		if errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return ErrNotAuthorized
		}
		if lookup.Error != nil {
			return lookup.Error
		}
		var grant model.PaperAccessGrant
		found := tx.Where("user_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)", i.OwnerID).Take(&grant)
		if errors.Is(found.Error, gorm.ErrRecordNotFound) {
			return ErrNotAuthorized
		}
		if found.Error != nil {
			return found.Error
		}

		// A duplicate HTTP retry must return the original job, never queue again.
		existing := tx.Where("project_id = ? AND idempotency_key = ?", i.ProjectID, i.IdempotencyKey).Limit(1).Find(&job)
		if existing.Error != nil {
			return existing.Error
		}
		if existing.RowsAffected != 0 {
			if !matching(job, i) {
				return ErrIdempotency
			}
			return nil
		}
		// A legitimate retry of an already accepted idempotency key remains
		// valid even if the document has since advanced to a newer revision.
		if project.HeadRevision != i.SourceRevision {
			return fmt.Errorf("%w: changed project revision", ErrInvalid)
		}
		job = model.PaperCompileJob{
			ID: uuid.New(), ProjectID: i.ProjectID, OwnerID: i.OwnerID, SourceRevision: i.SourceRevision,
			SnapshotDigest: i.SnapshotDigest, EnvironmentDigest: i.EnvironmentDigest,
			MainFile: i.MainFile, IdempotencyKey: i.IdempotencyKey, Priority: i.Priority,
			MaxAttempts: i.MaxAttempts, Status: StatusQueued,
		}
		// Unique index handles concurrent repeat submissions; DO NOTHING avoids
		// turning the entire PostgreSQL transaction into an aborted transaction.
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&job)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 0 {
			if err := tx.Where("project_id = ? AND idempotency_key = ?", i.ProjectID, i.IdempotencyKey).Take(&job).Error; err != nil {
				return err
			}
			if !matching(job, i) {
				return ErrIdempotency
			}
		}
		return nil
	})
	return job, err
}

type Claim struct {
	Kind       string
	Job        model.PaperCompileJob
	WorkerID   string
	Token      uuid.UUID
	Epoch      int64
	SandboxID  string
	LeaseUntil time.Time
}

func (c Claim) Valid() bool {
	return (c.Kind == ClaimRun || c.Kind == ClaimRecover) &&
		c.Job.ID != uuid.Nil && c.Token != uuid.Nil && c.Epoch > 0 &&
		workerPattern.MatchString(c.WorkerID) && c.SandboxID != ""
}
func dbTime(tx *gorm.DB) (time.Time, error) {
	var row struct{ Now time.Time }
	err := tx.Raw("SELECT CURRENT_TIMESTAMP AS now").Scan(&row).Error
	return row.Now, err
}
func (q *Queue) lockSlot(tx *gorm.DB) (model.PaperCompileSlot, bool, error) {
	var slot model.PaperCompileSlot
	result := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("id=1").Limit(1).Find(&slot)
	if result.Error != nil {
		return slot, false, result.Error
	}
	if result.RowsAffected == 0 {
		// Distinguish a locked row from missing migration seed. Plain SELECT
		// observes the row even while another transaction owns its row lock.
		var seeded int64
		if err := tx.Model(&model.PaperCompileSlot{}).Where("id = 1").Count(&seeded).Error; err != nil {
			return slot, false, err
		}
		if seeded != 1 {
			return slot, false, ErrNotReady
		}
		return slot, false, nil
	}
	return slot, true, nil
}
func (q *Queue) SlotState(ctx context.Context) (model.PaperCompileSlot, error) {
	var slot model.PaperCompileSlot
	err := q.db.WithContext(ctx).First(&slot, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return slot, ErrNotReady
	}
	return slot, err
}

func boundedFailureCode(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > 64 {
		return reason[:64]
	}
	return reason
}
