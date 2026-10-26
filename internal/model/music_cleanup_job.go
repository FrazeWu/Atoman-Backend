package model

import "time"

// MusicCleanupJob holds a failed S3 object deletion until a worker retries it.
// Music versioned-object keys are immutable and deleting the same key twice
// is safe, so a key can represent one idempotent cleanup request.
type MusicCleanupJob struct {
	Key           string     `gorm:"type:varchar(1024);primaryKey"`
	Attempts      int        `gorm:"not null;default:0"`
	NextAttemptAt time.Time  `gorm:"not null;index"`
	LeaseToken    string     `gorm:"type:varchar(64);not null;default:''"`
	LeaseUntil    *time.Time `gorm:"index"`
	LastError     string     `gorm:"type:text;not null;default:''"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (MusicCleanupJob) TableName() string { return "music_cleanup_jobs" }
