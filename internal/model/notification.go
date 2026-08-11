package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type NotificationMeta map[string]interface{}

func (m NotificationMeta) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (m *NotificationMeta) Scan(value interface{}) error {
	if value == nil {
		*m = NotificationMeta{}
		return nil
	}

	var raw []byte
	switch v := value.(type) {
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		return fmt.Errorf("NotificationMeta: unsupported scan type %T", value)
	}

	if len(raw) == 0 || string(raw) == "null" {
		*m = NotificationMeta{}
		return nil
	}

	return json.Unmarshal(raw, m)
}

type Notification struct {
	Base
	RecipientID    uuid.UUID        `json:"recipient_id" gorm:"type:uuid;not null;index"`
	Recipient      *User            `json:"recipient,omitempty" gorm:"foreignKey:RecipientID;references:UUID"`
	ActorID        *uuid.UUID       `json:"actor_id" gorm:"type:uuid;index"`
	Actor          *User            `json:"actor,omitempty" gorm:"foreignKey:ActorID;references:UUID"`
	Type           string           `json:"type" gorm:"not null"`
	SourceType     string           `json:"source_type" gorm:"not null"`
	SourceID       uuid.UUID        `json:"source_id" gorm:"type:uuid;not null"`
	AggregationKey string           `json:"aggregation_key" gorm:"not null;default:'';index"`
	Meta           NotificationMeta `json:"meta" gorm:"type:jsonb;default:'{}'"`
	ReadAt         *time.Time       `json:"read_at"`
}

func (Notification) TableName() string {
	return "notifications"
}

type NotificationPreference struct {
	Base
	UserID    uuid.UUID `json:"-" gorm:"type:uuid;not null;uniqueIndex:idx_notification_preference_scope,priority:1"`
	Category  string    `json:"category" gorm:"type:varchar(24);not null"`
	EventType string    `json:"event_type" gorm:"type:varchar(64);not null;uniqueIndex:idx_notification_preference_scope,priority:2"`
	Enabled   bool      `json:"enabled" gorm:"not null"`
}

func (NotificationPreference) TableName() string { return "notification_preferences" }

type NotificationMute struct {
	Base
	UserID     uuid.UUID `json:"-" gorm:"type:uuid;not null;uniqueIndex:idx_notification_mute_scope,priority:1"`
	SourceType string    `json:"source_type" gorm:"type:varchar(64);not null;uniqueIndex:idx_notification_mute_scope,priority:2"`
	SourceID   uuid.UUID `json:"source_id" gorm:"type:uuid;not null;uniqueIndex:idx_notification_mute_scope,priority:3"`
	Reason     string    `json:"reason" gorm:"type:varchar(255);not null;default:''"`
}

func (NotificationMute) TableName() string { return "notification_mutes" }
