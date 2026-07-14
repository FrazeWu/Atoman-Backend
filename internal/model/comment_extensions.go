package model

import (
	"encoding/json"

	"github.com/google/uuid"
)

type TimelineRevisionProposal struct {
	Base
	CommentID         uuid.UUID       `json:"comment_id" gorm:"type:uuid;not null;unique"`
	TargetKind        string          `json:"target_kind" gorm:"not null"`
	TargetID          uuid.UUID       `json:"target_id" gorm:"type:uuid;not null;index"`
	PatchJSON         json.RawMessage `json:"patch_json" gorm:"type:jsonb;not null"`
	Evidence          string          `json:"evidence" gorm:"type:text;not null"`
	Status            string          `json:"status" gorm:"not null;default:'pending';index"`
	ReviewerID        *uuid.UUID      `json:"reviewer_id,omitempty" gorm:"type:uuid"`
	AppliedRevisionID *uuid.UUID      `json:"applied_revision_id,omitempty" gorm:"type:uuid"`
}

type DebateArgumentDetail struct {
	CommentID     uuid.UUID `json:"comment_id" gorm:"type:uuid;primaryKey"`
	ArgumentType  string    `json:"argument_type" gorm:"not null"`
	SourceURL     string    `json:"source_url"`
	SourceTitle   string    `json:"source_title"`
	SourceExcerpt string    `json:"source_excerpt" gorm:"type:text"`
	Conclusion    string    `json:"conclusion" gorm:"type:text"`
}

type DebateArgumentReference struct {
	CommentID           uuid.UUID `json:"comment_id" gorm:"type:uuid;primaryKey"`
	ReferencedCommentID uuid.UUID `json:"referenced_comment_id" gorm:"type:uuid;primaryKey"`
}

type DebateArgumentDebateRef struct {
	CommentID uuid.UUID `json:"comment_id" gorm:"type:uuid;primaryKey"`
	DebateID  uuid.UUID `json:"debate_id" gorm:"type:uuid;primaryKey"`
}
