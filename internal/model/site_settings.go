package model

import "time"

// SiteSetting stores administrator-configurable key/value parameters.
// Keys are namespaced by module: "forum.xxx", "timeline.xxx", etc.
type SiteSetting struct {
	Key         string    `json:"key" gorm:"primaryKey"`
	Value       string    `json:"value" gorm:"not null"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}

func (SiteSetting) TableName() string { return "site_settings" }
