package model

import (
	"github.com/google/uuid"
	"time"
)

// Organizer records are private and contain only opaque JMAP IDs in indexes.
// All user-facing labels and reminders are protected in encrypted payloads.
type MailOrganizerRecord struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID     uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_mail_organizer_user_message"`
	EmailID    string    `gorm:"type:varchar(512);not null;uniqueIndex:idx_mail_organizer_user_message"`
	Ciphertext []byte    `gorm:"type:bytea;not null"`
	Revision   uint64    `gorm:"not null;default:1"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (MailOrganizerRecord) TableName() string { return "mail_organizer_records" }

// Atoman labels are local organizer metadata, not transferable IMAP keywords.
type MailOrganizerLabel struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID     uuid.UUID `gorm:"type:uuid;not null;index"`
	Ciphertext []byte    `gorm:"type:bytea;not null"`
	Revision   uint64    `gorm:"not null;default:1"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (MailOrganizerLabel) TableName() string { return "mail_organizer_labels" }
