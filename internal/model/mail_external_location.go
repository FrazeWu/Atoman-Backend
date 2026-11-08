package model

import (
	"github.com/google/uuid"
	"time"
)

// Tombstone is an audit record, never a command to delete source mail.
// Imported UID is bound to its original IMAP folder and UIDVALIDITY.
type MailExternalLocationCase struct {
	ID                  uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID              uuid.UUID `gorm:"type:uuid;not null;index:idx_mail_location_user"`
	ExternalAccountID   uuid.UUID `gorm:"type:uuid;not null"`
	JobID               uuid.UUID `gorm:"type:uuid;not null;index:idx_mail_location_job"`
	ImportItemID        uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_mail_location_import_item"`
	SourceUID           uint32    `gorm:"not null"`
	UIDValidity         uint32    `gorm:"not null"`
	NativeEmailID       string    `gorm:"type:varchar(512);not null"`
	Status              string    `gorm:"type:varchar(32);not null"`
	MissingObservations uint32    `gorm:"not null;default:1"`
	FirstMissingAt      time.Time `gorm:"not null"`
	LastObservedAt      time.Time `gorm:"not null"`
	ReturnedAt          *time.Time
	LastCheckedAt       time.Time  `gorm:"not null"`
	CandidateItemID     *uuid.UUID `gorm:"type:uuid"`
	ActionID            *uuid.UUID `gorm:"type:uuid"`
	ResolvedAt          *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (MailExternalLocationCase) TableName() string { return "mail_external_location_cases" }

// Durable, user-scoped receipt for a recoverable native-only mailbox move.
// The Before/After snapshot is authenticated ciphertext, never returned to UI.
// An unknown Email/set outcome is not repeated: reconciliation is read-only.
type MailExternalLocationAction struct {
	ID                    uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID                uuid.UUID `gorm:"type:uuid;not null;index:idx_mail_location_action_user"`
	ExternalAccountID     uuid.UUID `gorm:"type:uuid;not null"`
	JobID                 uuid.UUID `gorm:"type:uuid;not null"`
	CaseID                uuid.UUID `gorm:"type:uuid;not null;index:idx_mail_location_action_case"`
	ImportItemID          uuid.UUID `gorm:"type:uuid;not null"`
	NativeEmailID         string    `gorm:"type:varchar(512);not null"`
	Action                string    `gorm:"type:varchar(24);not null"`
	BeforeAfterCiphertext []byte    `gorm:"type:bytea;not null"`
	Status                string    `gorm:"type:varchar(24);not null"`
	ErrorCode             string    `gorm:"type:varchar(48);not null;default:''"`
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (MailExternalLocationAction) TableName() string { return "mail_external_location_actions" }
