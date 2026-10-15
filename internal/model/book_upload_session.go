package model

import (
	"time"

	"github.com/google/uuid"
)

type BookUploadSession struct {
	Base
	AssetID                     uuid.UUID  `json:"asset_id" gorm:"type:uuid;not null;index"`
	UserID                      uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	OriginalFilename            string     `json:"file_name" gorm:"not null"`
	Format                      string     `json:"format" gorm:"not null"`
	ContentType                 string     `json:"content_type" gorm:"not null"`
	SizeBytes                   int64      `json:"size" gorm:"not null"`
	ObjectKey                   string     `json:"-" gorm:"not null"`
	UploadID                    string     `json:"-" gorm:"not null"`
	PartSize                    int64      `json:"part_size" gorm:"not null"`
	CompletedPartsJSON          string     `json:"-" gorm:"type:jsonb;not null;default:'[]'"`
	ExpiresAt                   time.Time  `json:"expires_at" gorm:"not null;index"`
	Status                      string     `json:"status" gorm:"not null;index;check:chk_book_upload_session_status,status IN ('uploading','completing','uploaded','scanning','ready','failed','quarantined')"`
	RevisionID                  *uuid.UUID `json:"revision_id,omitempty" gorm:"type:uuid"`
	ErrorMessage                string     `json:"error_message" gorm:"not null;default:''"`
	PublicDistributionConfirmed bool       `json:"-" gorm:"not null"`
}

func (BookUploadSession) TableName() string { return "book_upload_sessions" }
