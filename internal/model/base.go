package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Base implements the UUIDv7 primary key logic for all models
type Base struct {
	ID        uuid.UUID      `json:"id" gorm:"type:uuid;primaryKey"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

// BeforeCreate hook to generate UUIDv7
func (base *Base) BeforeCreate(tx *gorm.DB) error {
	if base.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		base.ID = id
	}
	return nil
}
