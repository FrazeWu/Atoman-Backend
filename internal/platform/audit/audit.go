package audit

import (
	"encoding/json"
	"fmt"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Entry struct {
	ActorID    *uuid.UUID
	Action     string
	EntityType string
	EntityID   *uuid.UUID
	Reason     string
	Metadata   map[string]any
}

func Record(db *gorm.DB, entry Entry) error {
	if strings.TrimSpace(entry.Action) == "" {
		return fmt.Errorf("audit action is required")
	}
	if strings.TrimSpace(entry.EntityType) == "" {
		return fmt.Errorf("audit entity_type is required")
	}

	metadata := "{}"
	if len(entry.Metadata) > 0 {
		bytes, err := json.Marshal(entry.Metadata)
		if err != nil {
			return err
		}
		metadata = string(bytes)
	}
	log := model.AuditLog{
		ActorID:    entry.ActorID,
		Action:     entry.Action,
		EntityType: entry.EntityType,
		EntityID:   entry.EntityID,
		Reason:     entry.Reason,
		Metadata:   metadata,
	}
	return db.Create(&log).Error
}
