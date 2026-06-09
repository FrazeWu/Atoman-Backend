package model

import "github.com/google/uuid"

type MusicEdit struct {
	Base
	Type          string     `json:"type" gorm:"not null;index"`
	EntityType    string     `json:"entity_type" gorm:"not null;index"`
	EntityID      *uuid.UUID `json:"entity_id,omitempty" gorm:"type:uuid;index"`
	SubmittedBy   uuid.UUID  `json:"submitted_by" gorm:"type:uuid;not null;index"`
	SubmittedUser *User      `json:"submitted_user,omitempty" gorm:"foreignKey:SubmittedBy;references:UUID"`
	Status        string     `json:"status" gorm:"not null;default:'open';index"`
	Reason        string     `json:"reason" gorm:"type:text;not null"`
	PayloadJSON   string     `json:"payload" gorm:"type:jsonb;default:'{}'"`
	ChangesJSON   string     `json:"changes" gorm:"type:jsonb;default:'{}'"`
	SourcesJSON   string     `json:"sources" gorm:"type:jsonb;default:'[]'"`
	AutoApplied   bool       `json:"auto_applied" gorm:"default:false"`
	Votable       bool       `json:"votable" gorm:"default:true"`
	FailureReason string     `json:"failure_reason" gorm:"type:text"`
}

func (MusicEdit) TableName() string { return "music_edits" }

type MusicEditVote struct {
	Base
	EditID  uuid.UUID `json:"edit_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_edit_vote_user,priority:1"`
	UserID  uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_edit_vote_user,priority:2"`
	Vote    string    `json:"vote" gorm:"not null"`
	Comment string    `json:"comment" gorm:"type:text"`
}

func (MusicEditVote) TableName() string { return "music_edit_votes" }

type MusicEditDecision struct {
	Base
	EditID    uuid.UUID `json:"edit_id" gorm:"type:uuid;not null;index"`
	DeciderID uuid.UUID `json:"decider_id" gorm:"type:uuid;not null;index"`
	Decision  string    `json:"decision" gorm:"not null"`
	Reason    string    `json:"reason" gorm:"type:text;not null"`
}

func (MusicEditDecision) TableName() string { return "music_edit_decisions" }

type MusicEditChange struct {
	Base
	EditID     uuid.UUID  `json:"edit_id" gorm:"type:uuid;not null;index"`
	EntityType string     `json:"entity_type" gorm:"not null;index"`
	EntityID   *uuid.UUID `json:"entity_id,omitempty" gorm:"type:uuid;index"`
	BeforeJSON string     `json:"before" gorm:"type:jsonb;default:'{}'"`
	AfterJSON  string     `json:"after" gorm:"type:jsonb;default:'{}'"`
}

func (MusicEditChange) TableName() string { return "music_edit_changes" }
