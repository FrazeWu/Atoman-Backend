package model

import (
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type AuditLog struct {
	Base
	ActorID    *uuid.UUID `json:"actor_id" gorm:"type:uuid;index"`
	Action     string     `json:"action" gorm:"not null;index"`
	EntityType string     `json:"entity_type" gorm:"not null;index"`
	EntityID   *uuid.UUID `json:"entity_id" gorm:"type:uuid;index"`
	Reason     string     `json:"reason" gorm:"type:text"`
	Metadata   string     `json:"metadata" gorm:"type:text"`
}

func (AuditLog) TableName() string { return "audit_logs" }

func (log *AuditLog) BeforeDelete(tx *gorm.DB) error {
	return errors.New("audit logs are append-only and cannot be deleted")
}
