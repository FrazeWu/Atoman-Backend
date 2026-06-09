package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// EmailVerificationCode represents an email verification code stored in database
type EmailVerificationCode struct {
	UUID      uuid.UUID      `json:"uuid" gorm:"type:uuid;primaryKey"`
	Email     string         `json:"email" gorm:"uniqueIndex;not null"`
	Code      string         `json:"-" gorm:"not null"`
	ExpiresAt time.Time      `json:"expires_at" gorm:"not null"`
	Used      bool           `json:"used" gorm:"default:false"`
	CreatedAt time.Time      `json:"created_at"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

func (EmailVerificationCode) TableName() string {
	return "email_verification_codes"
}

func (m *EmailVerificationCode) BeforeCreate(tx *gorm.DB) error {
	if m.UUID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		m.UUID = id
	}
	return nil
}
