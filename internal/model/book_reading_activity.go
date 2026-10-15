package model

import (
	"time"

	"github.com/google/uuid"
)

// One clock per reader prevents concurrent devices from claiming the same time.
type BookReaderClock struct {
	UserID     uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	SessionID  uuid.UUID `json:"-" gorm:"type:uuid;not null"`
	RevisionID uuid.UUID `json:"-" gorm:"type:uuid;not null"`
	LastSeenAt time.Time `json:"-" gorm:"not null"`
	Active     bool      `json:"-" gorm:"not null"`
}

func (BookReaderClock) TableName() string { return "book_reader_clocks" }

type BookReadingDaily struct {
	UserID     uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	RevisionID uuid.UUID `json:"revision_id" gorm:"type:uuid;primaryKey"`
	Day        string    `json:"day" gorm:"type:char(10);primaryKey"`
	Seconds    int64     `json:"seconds" gorm:"not null;check:chk_book_reading_seconds,seconds >= 0"`
}

func (BookReadingDaily) TableName() string { return "book_reading_daily" }

// The content itself lives only in ContentEntry + ContentBlogExtension.
// Keep this private receipt even if canonical content is later deleted; a retry
// must return its original identity, not publish a new copy after withdrawal.
type BookNotePublication struct {
	UserID      uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	RequestID   uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	NoteID      uuid.UUID `json:"-" gorm:"type:uuid;not null;index"`
	NoteVersion int64     `json:"-" gorm:"not null"`
	WorkID      uuid.UUID `json:"-" gorm:"type:uuid;not null;index"`
	ContentID   uuid.UUID `json:"content_id" gorm:"type:uuid;not null;uniqueIndex"`
	RequestHash string    `json:"-" gorm:"not null"`
	CreatedAt   time.Time `json:"published_at"`
}

func (BookNotePublication) TableName() string { return "book_note_publications" }
