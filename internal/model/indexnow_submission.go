package model

import "time"

// IndexNowSubmission is an at-least-once outbox for public URL updates.
// URL is unique, so repeated publication/deletion events coalesce safely.
type IndexNowSubmission struct {
	URL           string     `gorm:"type:varchar(2048);primaryKey"`
	Revision      int64      `gorm:"not null;default:1"`
	Attempts      int        `gorm:"not null;default:0"`
	NextAttemptAt time.Time  `gorm:"not null;index"`
	LeaseToken    string     `gorm:"type:varchar(64);not null;default:''"`
	LeaseUntil    *time.Time `gorm:"index"`
	LastError     string     `gorm:"type:text;not null;default:''"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (IndexNowSubmission) TableName() string { return "indexnow_submissions" }
