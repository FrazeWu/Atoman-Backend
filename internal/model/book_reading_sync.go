package model

import (
	"time"

	"github.com/google/uuid"
)

type BookReadingSession struct {
	Base
	UserID          uuid.UUID `json:"-" gorm:"type:uuid;not null;uniqueIndex:idx_book_reading_session_client,priority:1"`
	ClientSessionID uuid.UUID `json:"client_session_id" gorm:"type:uuid;not null;uniqueIndex:idx_book_reading_session_client,priority:2"`
	RevisionID      uuid.UUID `json:"revision_id" gorm:"type:uuid;not null;index"`
	DeviceID        string    `json:"device_id" gorm:"type:varchar(128);not null"`
	LastSequence    int64     `json:"last_sequence" gorm:"not null;default:0;check:chk_book_reading_session_sequence,last_sequence >= 0"`
}

func (BookReadingSession) TableName() string { return "book_reading_sessions" }

// Receipts are scoped to user + immutable revision + request ID. Replaying a
// receipt returns its original acknowledgment without touching current state.
type BookReadingWrite struct {
	UserID      uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	RevisionID  uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	RequestID   uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	SessionID   uuid.UUID `json:"-" gorm:"type:uuid;not null"`
	RequestHash string    `json:"-" gorm:"type:varchar(64);not null"`
	ResultJSON  string    `json:"-" gorm:"type:jsonb;not null"`
	CreatedAt   time.Time `json:"-"`
}

func (BookReadingWrite) TableName() string { return "book_reading_writes" }
