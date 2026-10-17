package model

import (
	"time"

	"github.com/google/uuid"
)

// A passage identifies a public selection in one immutable file revision. It
// never contains a reader's private annotation, quote, note or reading state.
type BookDiscussionPassage struct {
	Base
	RevisionID   uuid.UUID `json:"revision_id" gorm:"type:uuid;not null;uniqueIndex:idx_book_passage_anchor,priority:1"`
	AnchorJSON   string    `json:"-" gorm:"type:jsonb;not null"`
	AnchorDigest string    `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_book_passage_anchor,priority:2"`
	CreatedBy    uuid.UUID `json:"-" gorm:"type:uuid;not null"`
}

func (BookDiscussionPassage) TableName() string { return "book_discussion_passages" }

// Body, publication state and author remain authoritative in ContentEntry and
// its blog extension. One long review belongs to one original work; merges
// group aliases at query time, not by rewriting this relation or the content.
type BookContentReview struct {
	// A logical canonical-content reference, not a second content authority.
	// A deleted blog may leave this removable relation; public reads join and
	// check the live content entry instead of trusting cached publication data.
	ContentID uuid.UUID `json:"content_id" gorm:"type:uuid;primaryKey"`
	WorkID    uuid.UUID `json:"work_id" gorm:"type:uuid;not null;index"`
	CreatedBy uuid.UUID `json:"-" gorm:"type:uuid;not null"`
	Spoiler   bool      `json:"spoiler" gorm:"not null;default:false"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (BookContentReview) TableName() string { return "book_content_reviews" }
