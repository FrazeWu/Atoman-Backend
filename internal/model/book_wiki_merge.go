package model

import (
	"time"

	"github.com/google/uuid"
)

// Merge snapshots can contain private reviews/selections. Never serialize them
// into API responses; they are only an undo journal for transactional recovery.
type BookWikiMerge struct {
	Base
	SourceID         uuid.UUID  `json:"source_id" gorm:"type:uuid;not null;index"`
	TargetID         uuid.UUID  `json:"target_id" gorm:"type:uuid;not null;index"`
	CreatedBy        uuid.UUID  `json:"created_by" gorm:"type:uuid;not null"`
	Status           string     `json:"status" gorm:"not null;default:'merged';check:chk_book_wiki_merge_status,status IN ('merged','reverted')"`
	BeforeJSON       string     `json:"-" gorm:"type:jsonb;not null"`
	AfterJSON        string     `json:"-" gorm:"type:jsonb;not null"`
	SourceRevisionID uuid.UUID  `json:"source_revision_id" gorm:"type:uuid;not null"`
	TargetRevisionID uuid.UUID  `json:"target_revision_id" gorm:"type:uuid;not null"`
	RevertedBy       *uuid.UUID `json:"reverted_by,omitempty" gorm:"type:uuid"`
	RevertedAt       *time.Time `json:"reverted_at,omitempty"`
}

func (BookWikiMerge) TableName() string { return "book_wiki_merges" }
