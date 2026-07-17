package model

import (
	"time"

	"github.com/google/uuid"
)

// ForumUserTrust stores the highest forum trust level a user has reached.
type ForumUserTrust struct {
	UserID      uuid.UUID `json:"user_id" gorm:"type:uuid;primaryKey"`
	Level       int       `json:"level" gorm:"not null;default:0"`
	EvaluatedAt time.Time `json:"evaluated_at" gorm:"not null"`
}

func (ForumUserTrust) TableName() string { return "forum_user_trust" }
