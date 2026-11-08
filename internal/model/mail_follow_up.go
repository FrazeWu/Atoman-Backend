package model

import (
	"github.com/google/uuid"
	"time"
)

// Only the due timestamp and opaque source ID are indexed in plaintext.
// Message body, subject, labels and full reminder content stay encrypted in
// mail_organizer_records. The scheduler needs due_at to run while offline.
type MailFollowUpSchedule struct {
	RecordID          uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID            uuid.UUID `gorm:"type:uuid;not null;index:idx_mail_followup_user_due"`
	DueAt             time.Time `gorm:"not null;index:idx_mail_followup_user_due;index:idx_mail_followup_due"`
	OrganizerRevision uint64    `gorm:"not null"`
	DeliveredAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (MailFollowUpSchedule) TableName() string { return "mail_followup_schedules" }

// Rolling cursor for migrating old encrypted reminders into the due index.
// New reminder mutations always update the index in the same DB transaction.
type MailFollowUpBackfillState struct {
	Key          string    `gorm:"type:varchar(32);primaryKey"`
	LastRecordID uuid.UUID `gorm:"type:uuid"`
	Completed    bool      `gorm:"not null;default:false"`
	UpdatedAt    time.Time
}

func (MailFollowUpBackfillState) TableName() string { return "mail_followup_backfill_state" }
