package model

import (
	"time"

	"github.com/google/uuid"
)

// BookEditionBestFile is an edition-scoped recommendation. It never edits the
// chosen file, the user's bookshelf, or the user's reading state.
type BookEditionBestFile struct {
	EditionID  uuid.UUID `json:"edition_id" gorm:"type:uuid;primaryKey"`
	AssetID    uuid.UUID `json:"asset_id" gorm:"type:uuid;not null"`
	RevisionID uuid.UUID `json:"revision_id" gorm:"type:uuid;not null"`
	UpdatedBy  uuid.UUID `json:"updated_by" gorm:"type:uuid;not null"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (BookEditionBestFile) TableName() string { return "book_edition_best_files" }

const (
	BookFileShelfUnstarted = "unstarted"
	BookFileShelfReading   = "reading"
	BookFileShelfFinished  = "finished"
	BookFileShelfPaused    = "paused"
)

// BookFileShelf pins a single immutable file revision, not the abstract work.
// Removing it never deletes the file, notes, or private reading history.
type BookFileShelf struct {
	Base
	UserID     uuid.UUID `json:"-" gorm:"type:uuid;not null;uniqueIndex:idx_book_file_shelf_user_revision,priority:1"`
	RevisionID uuid.UUID `json:"revision_id" gorm:"type:uuid;not null;uniqueIndex:idx_book_file_shelf_user_revision,priority:2"`
	Status     string    `json:"status" gorm:"not null;default:'unstarted';check:chk_book_file_shelf_status,status IN ('unstarted','reading','finished','paused')"`
	Archived   bool      `json:"archived" gorm:"not null;default:false"`
	Note       string    `json:"note,omitempty" gorm:"type:text;not null;default:''"`
}

func (BookFileShelf) TableName() string { return "book_file_shelves" }

// BookBestFileEvent records changes to the recommended file. Personal shelf
// entries and progress remain untouched when a recommendation changes.
type BookBestFileEvent struct {
	Base
	EditionID      uuid.UUID  `json:"edition_id" gorm:"type:uuid;not null;index"`
	FromRevisionID *uuid.UUID `json:"from_revision_id,omitempty" gorm:"type:uuid"`
	ToRevisionID   uuid.UUID  `json:"to_revision_id" gorm:"type:uuid;not null"`
	ChangedBy      uuid.UUID  `json:"changed_by" gorm:"type:uuid;not null"`
}

func (BookBestFileEvent) TableName() string { return "book_best_file_events" }

// BookEditionCover stores the private object key separately from public metadata.
type BookEditionCover struct {
	EditionID   uuid.UUID `json:"edition_id" gorm:"type:uuid;primaryKey"`
	ObjectKey   string    `json:"-" gorm:"type:text;not null"`
	ContentType string    `json:"content_type" gorm:"not null"`
	UpdatedBy   uuid.UUID `json:"updated_by" gorm:"type:uuid;not null"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (BookEditionCover) TableName() string { return "book_edition_covers" }

// BookFileVisibility is separate from the publicly visible catalog metadata.
// Uploads remain owner-only unless the uploader explicitly opts into sharing.
type BookFileVisibility struct {
	RevisionID uuid.UUID `json:"revision_id" gorm:"type:uuid;primaryKey"`
	Public     bool      `json:"public" gorm:"not null;default:false"`
	UpdatedBy  uuid.UUID `json:"-" gorm:"type:uuid;not null"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (BookFileVisibility) TableName() string { return "book_file_visibilities" }

// BookFileQuality stores bounded, factual inspection signals for a single immutable
// revision. No format ranking or unverified OCR/completeness claim is persisted.
type BookFileQuality struct {
	RevisionID uuid.UUID `json:"revision_id" gorm:"type:uuid;primaryKey"`
	ReportJSON string    `json:"-" gorm:"type:jsonb;not null"`
	CreatedAt  time.Time `json:"created_at"`
}

func (BookFileQuality) TableName() string { return "book_file_qualities" }
