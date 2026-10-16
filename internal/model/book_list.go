package model

import (
	"time"

	"github.com/google/uuid"
)

// Draft and published snapshots are separate states. Public readers must never
// receive DraftJSON, the editor's CAS version, or private draft timestamps.
type BookList struct {
	Base
	UserID           uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	Version          int64      `json:"-" gorm:"not null;check:chk_book_list_version,version > 0"`
	DraftJSON        string     `json:"-" gorm:"type:jsonb;not null"`
	PublishedJSON    string     `json:"-" gorm:"type:jsonb;not null;default:'{}'"`
	PublishedVersion int64      `json:"-" gorm:"not null;default:0;check:chk_book_list_published_version,published_version >= 0"`
	Visibility       string     `json:"-" gorm:"not null;default:'private';check:chk_book_list_visibility,visibility IN ('public','private')"`
	ChangesJSON      string     `json:"-" gorm:"type:jsonb;not null;default:'{}'"`
	PublishedAt      *time.Time `json:"-"`
}

func (BookList) TableName() string { return "book_lists" }

// A private bookmark survives withdrawal of a list, but cannot grant access to
// it. SeenVersion only advances when the reader explicitly acknowledges it.
type BookListBookmark struct {
	UserID      uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	ListID      uuid.UUID `json:"list_id" gorm:"type:uuid;primaryKey"`
	SeenVersion int64     `json:"seen_version" gorm:"not null;default:0;check:chk_book_list_seen_version,seen_version >= 0"`
	CreatedAt   time.Time `json:"created_at"`
}

func (BookListBookmark) TableName() string { return "book_list_bookmarks" }
