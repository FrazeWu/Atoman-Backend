package model

import (
	"github.com/google/uuid"
	"time"
)

// Private user-owned external mailbox state. No password, address or message
// metadata is stored in plaintext; ciphertext is bound to owner and record.
type MailExternalAccount struct {
	ID                   uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID               uuid.UUID `gorm:"type:uuid;not null;index:idx_mail_external_user"`
	Provider             string    `gorm:"type:varchar(20);not null"`
	CredentialCiphertext []byte    `gorm:"type:bytea;not null"`
	SnapshotCiphertext   []byte    `gorm:"type:bytea"`
	Revision             uint64    `gorm:"not null;default:1"`
	LastSyncedAt         *time.Time
	LastAttemptAt        *time.Time
	LastErrorCode        string `gorm:"type:varchar(32);not null;default:''"`
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (MailExternalAccount) TableName() string { return "mail_external_accounts" }
