package model

import (
	"time"

	"github.com/google/uuid"
)

type VideoImportSession struct {
	Base
	UserID             uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	ChannelID          *uuid.UUID `json:"channel_id,omitempty" gorm:"type:uuid;index"`
	Status             string     `json:"status" gorm:"not null;default:'pending_upload';index"`
	FileName           string     `json:"file_name" gorm:"not null"`
	FileSize           int64      `json:"file_size" gorm:"not null"`
	ContentType        string     `json:"content_type" gorm:"not null"`
	ObjectKey          string     `json:"-" gorm:"type:text;not null"`
	UploadID           string     `json:"-" gorm:"type:text;not null"`
	PartSize           int64      `json:"part_size" gorm:"not null"`
	CompletedPartsJSON string     `json:"-" gorm:"type:text;not null;default:'[]'"`
	PayloadJSON        string     `json:"-" gorm:"type:text;not null;default:'{}'"`
	PublishMode        string     `json:"publish_mode" gorm:"not null;default:''"`
	PublishRequestedAt *time.Time `json:"publish_requested_at,omitempty"`
	UploadCompletedAt  *time.Time `json:"upload_completed_at,omitempty"`
	ScheduledAt        *time.Time `json:"scheduled_at,omitempty"`
	ErrorMessage       string     `json:"error_message" gorm:"type:text;not null;default:''"`
	ContentID          *uuid.UUID `json:"content_id,omitempty" gorm:"type:uuid;index"`
	TargetVideoID      *uuid.UUID `json:"target_video_id,omitempty" gorm:"type:uuid;index"`
	TargetVideo        *Video     `json:"-" gorm:"-"`
}

func (VideoImportSession) TableName() string { return "video_import_sessions" }
