package model

import (
	"time"

	"github.com/google/uuid"
)

type UserStudioState struct {
	UserID    uuid.UUID  `json:"user_id" gorm:"type:uuid;primaryKey"`
	ChannelID *uuid.UUID `json:"channel_id,omitempty" gorm:"type:uuid;index"`
	Channel   *Channel   `json:"channel,omitempty" gorm:"foreignKey:ChannelID"`
}

func (UserStudioState) TableName() string { return "user_studio_states" }

type StudioModuleSettings struct {
	Base
	UserID               uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	ChannelID            uuid.UUID  `json:"channel_id" gorm:"type:uuid;not null;index"`
	ContentType          string     `json:"content_type" gorm:"type:varchar(16);not null;index"`
	DefaultCollectionID  *uuid.UUID `json:"default_collection_id,omitempty" gorm:"type:uuid;index"`
	DefaultVisibility    string     `json:"default_visibility" gorm:"not null;default:'public'"`
	DefaultPublishStatus string     `json:"default_publish_status" gorm:"not null;default:'draft'"`
	AutoplayEnabled      bool       `json:"autoplay_enabled" gorm:"not null;default:false"`
}

func (StudioModuleSettings) TableName() string { return "studio_module_settings" }

type StudioMetricEvent struct {
	Base
	ChannelID   uuid.UUID `json:"channel_id" gorm:"type:uuid;not null;index"`
	ContentType string    `json:"content_type" gorm:"type:varchar(16);not null;index"`
	ContentID   uuid.UUID `json:"content_id" gorm:"type:uuid;not null;index"`
	Metric      string    `json:"metric" gorm:"type:varchar(16);not null;index"`
}

func (StudioMetricEvent) TableName() string { return "studio_metric_events" }

// StudioInteractionState stores a channel owner's workflow state without changing public comment state.
type StudioInteractionState struct {
	ChannelID uuid.UUID  `json:"channel_id" gorm:"type:uuid;primaryKey"`
	CommentID uuid.UUID  `json:"comment_id" gorm:"type:uuid;primaryKey"`
	Handled   bool       `json:"handled" gorm:"not null;default:false;index"`
	Priority  string     `json:"priority" gorm:"type:varchar(16);not null;default:'normal';index"`
	HandledBy *uuid.UUID `json:"handled_by,omitempty" gorm:"type:uuid;index"`
	HandledAt *time.Time `json:"handled_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (StudioInteractionState) TableName() string { return "studio_interaction_states" }

// StudioReplyTemplate is a channel-scoped reusable reply draft for Studio interactions.
type StudioReplyTemplate struct {
	Base
	ChannelID uuid.UUID `json:"channel_id" gorm:"type:uuid;not null;index"`
	CreatedBy uuid.UUID `json:"created_by" gorm:"type:uuid;not null;index"`
	Name      string    `json:"name" gorm:"not null"`
	Content   string    `json:"content" gorm:"type:text;not null"`
}

func (StudioReplyTemplate) TableName() string { return "studio_reply_templates" }
