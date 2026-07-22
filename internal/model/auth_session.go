package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type AuthSession struct {
	ID           uuid.UUID  `json:"id" gorm:"type:uuid;primaryKey"`
	UserID       uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	TokenHash    string     `json:"-" gorm:"size:64;not null;uniqueIndex"`
	CSRFHash     string     `json:"-" gorm:"size:64;not null;default:''"`
	Kind         string     `json:"kind" gorm:"size:16;not null;index"`
	UserAgent    string     `json:"-" gorm:"size:512;not null;default:''"`
	IPPrefix     string     `json:"-" gorm:"size:64;not null;default:''"`
	LastActiveAt time.Time  `json:"last_active_at" gorm:"not null;index"`
	ExpiresAt    time.Time  `json:"expires_at" gorm:"not null;index"`
	RevokedAt    *time.Time `json:"revoked_at" gorm:"index"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (AuthSession) TableName() string {
	return "auth_sessions"
}

func (session *AuthSession) BeforeCreate(tx *gorm.DB) error {
	if session.ID != uuid.Nil {
		return nil
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	session.ID = id
	return nil
}
