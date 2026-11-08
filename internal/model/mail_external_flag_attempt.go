package model

import (
	"github.com/google/uuid"
	"time"
)

// Immutable origin + expected flags for one conditional cross-provider write.
// A still-claimed receipt is NEVER auto-replayed after a crash or timeout.
type MailExternalFlagAttempt struct {
	ID                uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID            uuid.UUID `gorm:"type:uuid;not null;index:idx_mail_flag_attempt_user"`
	ExternalAccountID uuid.UUID `gorm:"type:uuid;not null"`
	JobID             uuid.UUID `gorm:"type:uuid;not null;index:idx_mail_flag_attempt_job"`
	ImportItemID      uuid.UUID `gorm:"type:uuid;not null;index:idx_mail_flag_attempt_item"`
	SourceUID         uint32    `gorm:"not null"`
	UIDValidity       uint32    `gorm:"not null"`
	NativeEmailID     string    `gorm:"type:varchar(512);not null"`
	Flag              string    `gorm:"type:varchar(12);not null"`
	Direction         string    `gorm:"type:varchar(24);not null"`
	ExpectedSource    bool      `gorm:"not null"`
	ExpectedNative    bool      `gorm:"not null"`
	Desired           bool      `gorm:"not null"`
	Status            string    `gorm:"type:varchar(24);not null"`
	ErrorCode         string    `gorm:"type:varchar(48);not null;default:''"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (MailExternalFlagAttempt) TableName() string { return "mail_external_flag_attempts" }
