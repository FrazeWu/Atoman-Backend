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
