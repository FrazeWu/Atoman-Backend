package model

import (
	"github.com/google/uuid"
	"time"
)

// Content is encrypted with the outbox key and authenticated to user+record.
type MailDraft struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID     uuid.UUID `gorm:"type:uuid;not null;index" json:"-"`
	Ciphertext []byte    `gorm:"type:bytea;not null" json:"-"`
	Revision   uint64    `gorm:"not null;default:1" json:"revision"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (MailDraft) TableName() string { return "mail_drafts" }

type MailUpload struct {
	ID          uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	UserID      uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	Name        string     `gorm:"type:varchar(255);not null" json:"name"`
	ContentType string     `gorm:"type:varchar(128);not null" json:"type"`
	Size        int64      `gorm:"not null" json:"size"`
	ObjectKey   string     `gorm:"type:varchar(255);not null;uniqueIndex" json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	RetiredAt   *time.Time `gorm:"index" json:"-"`
}

func (MailUpload) TableName() string { return "mail_uploads" }

type MailPreference struct {
	UserID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"-"`
	Ciphertext []byte    `gorm:"type:bytea;not null" json:"-"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (MailPreference) TableName() string { return "mail_preferences" }

type MailActionReceipt struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID     uuid.UUID `gorm:"type:uuid;not null;index"`
	Ciphertext []byte    `gorm:"type:bytea;not null"`
	ExpiresAt  time.Time `gorm:"not null;index"`
	UsedAt     *time.Time
	CreatedAt  time.Time
}

func (MailActionReceipt) TableName() string { return "mail_action_receipts" }

type MailRetention struct {
	UserID  uuid.UUID `gorm:"type:uuid;primaryKey"`
	EmailID string    `gorm:"type:varchar(512);primaryKey"`
	Role    string    `gorm:"type:varchar(16);not null"`
	MovedAt time.Time `gorm:"not null;index"`
}

func (MailRetention) TableName() string { return "mail_retention" }

// MailContact is a private per-user address-book record. The email address,
// name and notes are stored only inside authenticated ciphertext.
type MailContact struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID     uuid.UUID `gorm:"type:uuid;not null;index:idx_mail_contact_user_updated"`
	Ciphertext []byte    `gorm:"type:bytea;not null"`
	Revision   uint64    `gorm:"not null;default:1"`
	CreatedAt  time.Time
	UpdatedAt  time.Time `gorm:"index:idx_mail_contact_user_updated"`
}

func (MailContact) TableName() string { return "mail_contacts" }
