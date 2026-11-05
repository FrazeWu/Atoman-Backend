package model

import (
	"time"

	"github.com/google/uuid"
)

// MailAccount is the durable Atoman-user-to-address reservation. The actual
// mailbox, MIME messages, folders and IMAP credentials belong to Stalwart.
// No MailAccount can be marked active by the invitation feature.
type MailAccount struct {
	ID                uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID            uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_mail_accounts_user" json:"user_id"`
	LocalPart         string    `gorm:"type:varchar(32);not null;uniqueIndex:idx_mail_accounts_local" json:"local_part"`
	Address           string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_mail_accounts_address" json:"address"`
	Status            string    `gorm:"type:varchar(24);not null;default:'reserved'" json:"status"`
	StalwartAccountID *string   `gorm:"type:varchar(128);uniqueIndex:idx_mail_accounts_stalwart" json:"-"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (MailAccount) TableName() string { return "mail_accounts" }

// MailInvite stores only a SHA-256 hash of a high-entropy bearer token.
// Redeeming a token only reserves an address; it never activates a mailbox.
type MailInvite struct {
	ID           uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	TokenHash    string     `gorm:"type:char(64);not null;uniqueIndex:idx_mail_invites_token_hash" json:"-"`
	TargetUserID uuid.UUID  `gorm:"type:uuid;not null;index:idx_mail_invites_target" json:"target_user_id"`
	CreatedBy    uuid.UUID  `gorm:"type:uuid;not null" json:"created_by"`
	LocalPart    string     `gorm:"type:varchar(32);not null" json:"local_part"`
	ExpiresAt    time.Time  `gorm:"not null" json:"expires_at"`
	UsedAt       *time.Time `json:"used_at,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (MailInvite) TableName() string { return "mail_invites" }

// MailAccessCredential contains only encrypted per-user Stalwart API keys.
// Admin Stalwart keys are never placed in Atoman database or user sessions.
type MailAccessCredential struct {
	ID                uuid.UUID `gorm:"type:uuid;primaryKey" json:"-"`
	UserID            uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_mail_access_user" json:"-"`
	StalwartAccountID string    `gorm:"type:varchar(128);not null" json:"-"`
	APIKeyID          string    `gorm:"type:varchar(128);not null" json:"-"`
	Ciphertext        []byte    `gorm:"type:bytea;not null" json:"-"`
	Nonce             []byte    `gorm:"type:bytea;not null" json:"-"`
	KeyVersion        uint      `gorm:"not null;default:1" json:"-"`
	CreatedAt         time.Time `json:"-"`
	UpdatedAt         time.Time `json:"-"`
}

func (MailAccessCredential) TableName() string { return "mail_access_credentials" }

// MailPendingSend records a durable, server-side five-second Undo Send state.
// MIME and recipients live only in an encrypted private R2 snapshot.
type MailPendingSend struct {
	DraftID           *uuid.UUID `gorm:"type:uuid" json:"draft_id,omitempty"`
	DraftRevision     uint64     `gorm:"not null;default:0" json:"-"`
	ID                uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	UserID            uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_mail_pending_user_request;index:idx_mail_pending_user_state" json:"-"`
	RequestID         uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_mail_pending_user_request" json:"-"`
	AccountID         string     `gorm:"type:varchar(128);not null" json:"-"`
	SnapshotKey       string     `gorm:"type:varchar(255);not null;uniqueIndex:idx_mail_pending_snapshot" json:"-"`
	SnapshotDeletedAt *time.Time `json:"-"`
	PayloadHash       string     `gorm:"type:char(64);not null" json:"-"`
	Status            string     `gorm:"type:varchar(24);not null;index:idx_mail_pending_user_state;index:idx_mail_pending_due" json:"status"`
	SendAfter         time.Time  `gorm:"not null;index:idx_mail_pending_due" json:"send_after"`
	ImportedEmailID   string     `gorm:"type:varchar(512)" json:"-"`
	SubmissionID      string     `gorm:"type:varchar(512)" json:"-"`
	ClaimedAt         *time.Time `json:"-"`
	CancelledAt       *time.Time `json:"-"`
	SentAt            *time.Time `json:"-"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"-"`
}

func (MailPendingSend) TableName() string { return "mail_pending_sends" }
