package model

import (
	"time"

	"github.com/google/uuid"
)

type ContentLifecycleEvent struct {
	Base
	UserID        *uuid.UUID `json:"user_id,omitempty" gorm:"type:uuid;index"`
	ChannelID     uuid.UUID  `json:"channel_id" gorm:"type:uuid;not null;index"`
	ContentType   string     `json:"content_type" gorm:"type:varchar(16);not null;index"`
	ContentID     uuid.UUID  `json:"content_id" gorm:"type:uuid;not null;index"`
	Event         string     `json:"event" gorm:"type:varchar(24);not null;index"`
	Source        string     `json:"source" gorm:"type:varchar(32);not null;default:'direct';index"`
	SessionID     string     `json:"session_id" gorm:"type:varchar(64);not null;default:''"`
	ClientEventID string     `json:"client_event_id" gorm:"type:varchar(96);not null;uniqueIndex"`
	PositionSec   int        `json:"position_sec" gorm:"not null;default:0"`
	DurationSec   int        `json:"duration_sec" gorm:"not null;default:0"`
	Progress      float64    `json:"progress" gorm:"not null;default:0"`
}

func (ContentLifecycleEvent) TableName() string { return "content_lifecycle_events" }

type ContentProgress struct {
	Base
	UserID      uuid.UUID `json:"user_id" gorm:"type:uuid;not null;uniqueIndex:idx_content_progress_scope,priority:1"`
	ChannelID   uuid.UUID `json:"channel_id" gorm:"type:uuid;not null;index"`
	ContentType string    `json:"content_type" gorm:"type:varchar(16);not null;uniqueIndex:idx_content_progress_scope,priority:2"`
	ContentID   uuid.UUID `json:"content_id" gorm:"type:uuid;not null;uniqueIndex:idx_content_progress_scope,priority:3"`
	PositionSec int       `json:"position_sec" gorm:"not null;default:0"`
	DurationSec int       `json:"duration_sec" gorm:"not null;default:0"`
	Progress    float64   `json:"progress" gorm:"not null;default:0"`
	Completed   bool      `json:"completed" gorm:"not null;default:false;index"`
	Source      string    `json:"source" gorm:"type:varchar(32);not null;default:'direct'"`
}

func (ContentProgress) TableName() string { return "content_progress" }

type ContentNotificationPreference struct {
	Base
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;uniqueIndex:idx_content_notification_scope,priority:1"`
	SourceType string    `json:"source_type" gorm:"type:varchar(24);not null;uniqueIndex:idx_content_notification_scope,priority:2"`
	SourceID   uuid.UUID `json:"source_id" gorm:"type:uuid;not null;uniqueIndex:idx_content_notification_scope,priority:3"`
	Mode       string    `json:"mode" gorm:"type:varchar(16);not null;default:'feed_only'"`
}

func (ContentNotificationPreference) TableName() string { return "content_notification_preferences" }

type ContentPublicationEvent struct {
	Base
	ChannelID    uuid.UUID  `json:"channel_id" gorm:"type:uuid;not null;index"`
	OwnerID      uuid.UUID  `json:"owner_id" gorm:"type:uuid;not null;index"`
	ContentType  string     `json:"content_type" gorm:"type:varchar(16);not null;uniqueIndex:idx_content_publication,priority:1"`
	ContentID    uuid.UUID  `json:"content_id" gorm:"type:uuid;not null;uniqueIndex:idx_content_publication,priority:2"`
	Status       string     `json:"status" gorm:"type:varchar(16);not null;default:'pending';index"`
	Attempts     int        `json:"attempts" gorm:"not null;default:0"`
	LastError    string     `json:"last_error" gorm:"type:text"`
	DispatchedAt *time.Time `json:"dispatched_at,omitempty"`
}

func (ContentPublicationEvent) TableName() string { return "content_publication_events" }
