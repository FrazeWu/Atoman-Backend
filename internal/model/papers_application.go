package model

import (
	"github.com/google/uuid"
	"time"
)

// One durable application is maintained per account. Reviewing a pending
// request and granting access occur in the same database transaction.
type PaperAccessApplication struct {
	ID                     uuid.UUID  `json:"id" gorm:"type:uuid;primaryKey"`
	UserID                 uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;uniqueIndex"`
	Reason                 string     `json:"reason" gorm:"type:text;not null"`
	Status                 string     `json:"status" gorm:"type:varchar(16);not null;index"`
	ReviewerID             *uuid.UUID `json:"-" gorm:"type:uuid"`
	ReviewNote             string     `json:"review_note" gorm:"type:text;not null;default:''"`
	ReviewedAt             *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	NotificationStatus     string     `json:"notification_status" gorm:"type:varchar(16);not null;default:'';index"`
	NotificationRecipient  string     `json:"-" gorm:"type:varchar(320);not null;default:''"`
	NotificationAttempts   int        `json:"-" gorm:"not null;default:0"`
	NotificationNextAt     *time.Time `json:"-" gorm:"index"`
	NotificationFirstAt    *time.Time `json:"-"`
	NotificationLease      *uuid.UUID `json:"-" gorm:"type:uuid"`
	NotificationLeaseUntil *time.Time `json:"-"`
	NotificationSentAt     *time.Time `json:"notification_sent_at,omitempty"`
}

func (PaperAccessApplication) TableName() string { return "paper_access_applications" }
