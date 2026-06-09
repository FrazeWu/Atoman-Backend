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
	RecipientID uuid.UUID        `json:"recipient_id" gorm:"type:uuid;not null;index"`
	Recipient   *User            `json:"recipient,omitempty" gorm:"foreignKey:RecipientID;references:UUID"`
	ActorID     *uuid.UUID       `json:"actor_id" gorm:"type:uuid;index"`
	Actor       *User            `json:"actor,omitempty" gorm:"foreignKey:ActorID;references:UUID"`
	Type        string           `json:"type" gorm:"not null"`
	SourceType  string           `json:"source_type" gorm:"not null"`
	SourceID    uuid.UUID        `json:"source_id" gorm:"type:uuid;not null"`
	Meta        NotificationMeta `json:"meta" gorm:"type:jsonb;default:'{}'"`
	ReadAt      *time.Time       `json:"read_at"`
}

func (Notification) TableName() string {
	return "notifications"
}
