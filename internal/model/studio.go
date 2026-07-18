package model

import "github.com/google/uuid"

type UserStudioState struct {
	UserID    uuid.UUID  `json:"user_id" gorm:"type:uuid;primaryKey"`
	ChannelID *uuid.UUID `json:"channel_id,omitempty" gorm:"type:uuid;index"`
	Channel   *Channel   `json:"channel,omitempty" gorm:"foreignKey:ChannelID"`
}

func (UserStudioState) TableName() string { return "user_studio_states" }

type StudioModuleSettings struct {
	Base
	UserID               uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;uniqueIndex:idx_studio_settings_scope,priority:1"`
	ChannelID            uuid.UUID  `json:"channel_id" gorm:"type:uuid;not null;uniqueIndex:idx_studio_settings_scope,priority:2"`
	ContentType          string     `json:"content_type" gorm:"type:varchar(16);not null;uniqueIndex:idx_studio_settings_scope,priority:3"`
	DefaultCollectionID  *uuid.UUID `json:"default_collection_id,omitempty" gorm:"type:uuid;index"`
	DefaultVisibility    string     `json:"default_visibility" gorm:"not null;default:'public'"`
	DefaultPublishStatus string     `json:"default_publish_status" gorm:"not null;default:'published'"`
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
