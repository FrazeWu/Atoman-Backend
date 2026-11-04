package model

import (
	"github.com/google/uuid"
	"time"
)

type BookNoteCollection struct {
	Base
	UserID           uuid.UUID  `json:"-" gorm:"type:uuid;not null;index:idx_note_collection_owner_work,priority:1"`
	WorkID           uuid.UUID  `json:"work_id" gorm:"type:uuid;not null;index:idx_note_collection_owner_work,priority:2"`
	DraftJSON        string     `json:"-" gorm:"type:jsonb;not null;default:'{}'"`
	Version          int64      `json:"version" gorm:"not null;default:1"`
	PublishedVersion int64      `json:"published_version" gorm:"not null;default:0"`
	Visibility       string     `json:"visibility" gorm:"not null;default:'private';check:chk_note_collection_visibility,visibility IN ('private','link','public')"`
	ShareKey         string     `json:"-" gorm:"not null;uniqueIndex"`
	ActiveSnapshotID *uuid.UUID `json:"-" gorm:"type:uuid"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
}

func (BookNoteCollection) TableName() string { return "book_note_collections" }

type BookNoteShareSnapshot struct {
	Base
	CollectionID    uuid.UUID `json:"-" gorm:"type:uuid;not null;uniqueIndex:idx_note_snapshot_version,priority:1;uniqueIndex:idx_note_snapshot_request,priority:1"`
	Version         int64     `json:"version" gorm:"not null;uniqueIndex:idx_note_snapshot_version,priority:2"`
	RequestID       uuid.UUID `json:"-" gorm:"type:uuid;not null;uniqueIndex:idx_note_snapshot_request,priority:2"`
	RequestHash     string    `json:"-" gorm:"not null"`
	ContentJSON     string    `json:"-" gorm:"type:jsonb;not null"`
	SourceIndexJSON string    `json:"-" gorm:"type:jsonb;not null"`
}

func (BookNoteShareSnapshot) TableName() string { return "book_note_share_snapshots" }
