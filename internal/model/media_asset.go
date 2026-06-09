package model

import "github.com/google/uuid"

type MediaAsset struct {
	Base
	UserID      *uuid.UUID `json:"user_id,omitempty" gorm:"type:uuid;index"`
	Purpose     string     `json:"purpose" gorm:"not null;index"`
	URL         string     `json:"url" gorm:"type:text;not null"`
	Key         string     `json:"key" gorm:"type:text;not null"`
	ContentType string     `json:"content_type" gorm:"not null"`
	Size        int64      `json:"size" gorm:"not null"`
}

func (MediaAsset) TableName() string { return "media_assets" }
