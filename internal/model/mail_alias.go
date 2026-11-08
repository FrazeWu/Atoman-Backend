package model

import (
	"github.com/google/uuid"
	"time"
)

// A secondary atoman.org address belongs to one already active mailbox.
// Status needs_review is intentionally fail-closed after an uncertain remote
// JMAP change: a second attempt must not append another alias blindly.
type MailAlias struct {
	ID                uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID            uuid.UUID `gorm:"type:uuid;not null;index:idx_mail_alias_user"`
	LocalPart         string    `gorm:"type:varchar(32);not null;uniqueIndex:idx_mail_alias_local"`
	Address           string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_mail_alias_address"`
	StalwartAccountID string    `gorm:"type:varchar(128);not null"`
	Slot              uint32    `gorm:"not null;default:0"`
	Status            string    `gorm:"type:varchar(24);not null"`
	ErrorCode         string    `gorm:"type:varchar(48);not null;default:''"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (MailAlias) TableName() string { return "mail_aliases" }
