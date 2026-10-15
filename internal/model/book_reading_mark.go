package model

import "github.com/google/uuid"

// BookReadingMark belongs to one reader and one immutable file revision.
// Overlap groups are derived at read time; source quotes and notes are never discarded.
type BookReadingMark struct {
	Base
	UserID     uuid.UUID `json:"-" gorm:"type:uuid;not null;index:idx_book_mark_reader_revision,priority:1"`
	RevisionID uuid.UUID `json:"revision_id" gorm:"type:uuid;not null;index:idx_book_mark_reader_revision,priority:2"`
	Kind       string    `json:"kind" gorm:"not null;check:chk_book_mark_kind,kind IN ('bookmark','highlight')"`
	AnchorJSON string    `json:"-" gorm:"type:jsonb;not null"`
	Label      string    `json:"label" gorm:"not null;default:''"`
	Quote      string    `json:"quote" gorm:"type:text;not null;default:''"`
	Note       string    `json:"note" gorm:"type:text;not null;default:''"`
	Color      string    `json:"color" gorm:"not null;default:'';check:chk_book_mark_color,color IN ('','yellow','green','blue','pink')"`
	Style      string    `json:"style" gorm:"not null;default:'';check:chk_book_mark_style,style IN ('','highlight','underline')"`
	Version    int64     `json:"version" gorm:"not null;check:chk_book_mark_version,version > 0"`
}

func (BookReadingMark) TableName() string { return "book_reading_marks" }
