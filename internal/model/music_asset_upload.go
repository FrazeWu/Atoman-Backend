package model

import (
	"time"

	"github.com/google/uuid"
)

// MusicAssetUploadSession persists a resumable, user-owned audio upload before
// it is promoted to a MediaAsset.
type MusicAssetUploadSession struct {
	Base
	UserID             uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	Status             string     `json:"status" gorm:"not null;default:'uploading';index"`
	FileName           string     `json:"file_name" gorm:"not null"`
	ContentType        string     `json:"content_type" gorm:"not null"`
	Size               int64      `json:"size" gorm:"not null"`
	ObjectKey          string     `json:"-" gorm:"type:text;not null"`
	UploadID           string     `json:"-" gorm:"type:text;not null"`
	PartSize           int64      `json:"part_size" gorm:"not null"`
	CompletedPartsJSON string     `json:"completed_parts" gorm:"type:text;not null;default:'[]'"`
	ExpiresAt          time.Time  `json:"expires_at" gorm:"not null;index"`
	CompletedAt        *time.Time `json:"completed_at"`
	AssetID            *uuid.UUID `json:"asset_id" gorm:"type:uuid;index"`
	ErrorMessage       string     `json:"error_message" gorm:"type:text;not null;default:''"`
}

func (MusicAssetUploadSession) TableName() string {
	return "music_asset_upload_sessions"
}
