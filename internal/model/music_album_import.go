package model

import (
	"time"

	"github.com/google/uuid"
)

type AlbumImportSession struct {
	Base
	Status      string     `json:"status" gorm:"not null;default:'pending_upload'"`
	PayloadJSON string     `json:"payload" gorm:"type:text;not null"`
	CommittedAt *time.Time `json:"committed_at"`
	CommittedBy *uuid.UUID `json:"committed_by" gorm:"type:uuid"`
}

func (AlbumImportSession) TableName() string {
	return "music_album_import_sessions"
}
