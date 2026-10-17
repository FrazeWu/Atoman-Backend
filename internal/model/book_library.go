package model

import (
	"time"

	"github.com/google/uuid"
)

// BookLibraryEntry is a work-level private shelf; it never selects a file.
type BookLibraryEntry struct {
	Base
	UserID   uuid.UUID `json:"-" gorm:"type:uuid;not null;uniqueIndex:idx_book_library_user_work,priority:1"`
	WorkID   uuid.UUID `json:"work_id" gorm:"type:uuid;not null;uniqueIndex:idx_book_library_user_work,priority:2"`
	Status   string    `json:"status" gorm:"not null;check:chk_book_library_status,status IN ('want_to_read','reading','read','on_hold','dropped')"`
	TagsJSON string    `json:"-" gorm:"type:jsonb;not null;default:'[]'"`
	Version  int64     `json:"version" gorm:"not null;check:chk_book_library_version,version > 0"`
}

func (BookLibraryEntry) TableName() string { return "book_library_entries" }

// WorkID records the original work and is not rewritten by catalog merges.
// Canonical work filters resolve redirects; private edits therefore survive undo.
type BookPrivateNote struct {
	Base
	UserID           uuid.UUID  `json:"-" gorm:"type:uuid;not null;index:idx_book_private_note_reader_work,priority:1"`
	WorkID           uuid.UUID  `json:"work_id" gorm:"type:uuid;not null;index:idx_book_private_note_reader_work,priority:2"`
	SourceRevisionID *uuid.UUID `json:"source_revision_id,omitempty" gorm:"type:uuid"`
	Title            string     `json:"title" gorm:"not null;default:''"`
	Body             string     `json:"body" gorm:"type:text;not null;default:''"`
	TagsJSON         string     `json:"-" gorm:"type:jsonb;not null;default:'[]'"`
	Version          int64      `json:"version" gorm:"not null;check:chk_book_private_note_version,version > 0"`
	PurgedAt         *time.Time `json:"-"`
}

func (BookPrivateNote) TableName() string { return "book_private_notes" }

type BookNoteImportReceipt struct {
	UserID      uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	RequestID   uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	RequestHash string    `json:"-" gorm:"not null"`
	ResultJSON  string    `json:"-" gorm:"type:jsonb;not null"`
	CreatedAt   time.Time `json:"-"`
}

func (BookNoteImportReceipt) TableName() string { return "book_note_import_receipts" }
