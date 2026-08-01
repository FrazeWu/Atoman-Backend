package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	LoginResultSucceeded = "succeeded"
	LoginResultFailed    = "failed"
)

type LoginEvent struct {
	ID          uuid.UUID  `json:"id" gorm:"type:uuid;primaryKey"`
	UserID      uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index:idx_login_events_user_created,priority:1"`
	SessionID   *uuid.UUID `json:"session_id,omitempty" gorm:"type:uuid;index"`
	Method      string     `json:"method" gorm:"size:32;not null;index"`
	Result      string     `json:"result" gorm:"size:16;not null;index"`
	FailureCode string     `json:"failure_code,omitempty" gorm:"size:64"`
	IPAddress   string     `json:"ip_address" gorm:"size:45;not null;default:'';index"`
	IPPrefix    string     `json:"ip_prefix" gorm:"size:64;not null;default:''"`
	CountryCode string     `json:"country_code" gorm:"size:2;not null;default:''"`
	Region      string     `json:"region" gorm:"size:128;not null;default:''"`
	City        string     `json:"city" gorm:"size:128;not null;default:''"`
	UserAgent   string     `json:"user_agent" gorm:"size:512;not null;default:''"`
	CreatedAt   time.Time  `json:"created_at" gorm:"index:idx_login_events_user_created,priority:2"`
}

func (LoginEvent) TableName() string { return "auth_login_events" }

func (event *LoginEvent) BeforeCreate(tx *gorm.DB) error {
	if event.ID != uuid.Nil {
		return nil
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	event.ID = id
	return nil
}
