package model

import (
	"encoding/json"

	"github.com/google/uuid"
)

// UserTheme represents the theme configuration and custom scoped CSS for a user.
type UserTheme struct {
	Base
	UserID       uuid.UUID       `json:"user_id" gorm:"type:uuid;not null;uniqueIndex:idx_user_themes_user_id,where:deleted_at IS NULL"`
	User         *User           `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	IsEnabled    bool            `json:"is_enabled" gorm:"not null;default:true"`
	Mode         string          `json:"mode" gorm:"type:varchar(16);not null;default:'preset'"`
	PresetConfig json.RawMessage `json:"preset_config" gorm:"type:jsonb;not null;default:'{}'"`
	CustomCSS    string          `json:"custom_css" gorm:"type:text"`
	SanitizedCSS string          `json:"sanitized_css" gorm:"type:text"`
}

func (UserTheme) TableName() string {
	return "user_themes"
}
