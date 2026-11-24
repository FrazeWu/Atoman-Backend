package model

import (
	"time"

	"github.com/google/uuid"
)

// PaperCompileJob stores immutable compile inputs and a mutable, fenced execution
// state. Binary source snapshots/environment bundles are separate immutable assets.
// This P0 schema is deliberately not exposed via an HTTP route.
type PaperCompileJob struct {
	ID                uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID         uuid.UUID  `gorm:"type:uuid;not null;index:idx_paper_compile_dispatch,priority:1;uniqueIndex:idx_paper_compile_idempotency,priority:1"`
	OwnerID           uuid.UUID  `gorm:"type:uuid;not null;index"`
	SourceRevision    int64      `gorm:"not null"`
	SnapshotDigest    string     `gorm:"type:varchar(71);not null"`
	EnvironmentDigest string     `gorm:"type:varchar(71);not null"`
	MainFile          string     `gorm:"type:varchar(512);not null"`
	IdempotencyKey    string     `gorm:"type:varchar(96);not null;uniqueIndex:idx_paper_compile_idempotency,priority:2"`
	Priority          int        `gorm:"not null;default:0"`
	Status            string     `gorm:"type:varchar(24);not null;index:idx_paper_compile_dispatch,priority:2"`
	Attempts          int        `gorm:"not null;default:0"`
	MaxAttempts       int        `gorm:"not null;default:2"`
	SandboxID         string     `gorm:"type:varchar(64);not null;default:''"`
	WorkerID          string     `gorm:"type:varchar(96);not null;default:''"`
	LeaseToken        *uuid.UUID `gorm:"type:uuid"`
	LeaseEpoch        int64      `gorm:"not null;default:0"`
	CancelRequested   bool       `gorm:"not null;default:false"`
	PDFDigest         string     `gorm:"type:varchar(71);not null;default:''"`
	PDFSize           int64      `gorm:"not null;default:0"`
	FailureCode       string     `gorm:"type:varchar(64);not null;default:''"`
	StartedAt         *time.Time
	FinishedAt        *time.Time
	CreatedAt         time.Time `gorm:"not null;index:idx_paper_compile_dispatch,priority:3"`
	UpdatedAt         time.Time `gorm:"not null"`
}

func (PaperCompileJob) TableName() string { return "paper_compile_jobs" }

// PaperCompileSlot is a single database-wide lease for all heavyweight Papers
// work. Its ID=1 row MUST be seeded by migration before any worker can claim.
// The monotonic Epoch is never reset: it fences out old workers after recovery.
type PaperCompileSlot struct {
	ID             int16      `gorm:"primaryKey"`
	JobID          *uuid.UUID `gorm:"type:uuid"`
	LeaseToken     *uuid.UUID `gorm:"type:uuid"`
	WorkerID       string     `gorm:"type:varchar(96);not null;default:''"`
	Epoch          int64      `gorm:"not null;default:0"`
	LeaseExpiresAt *time.Time
	Blocked        bool      `gorm:"not null;default:false"`
	BlockReason    string    `gorm:"type:varchar(160);not null;default:''"`
	UpdatedAt      time.Time `gorm:"not null"`
}

func (PaperCompileSlot) TableName() string { return "paper_compile_slots" }
