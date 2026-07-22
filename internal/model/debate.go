package model

import (
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Debate represents a structured debate topic
type Debate struct {
	Base
	UserID                   uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	User                     *User          `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Title                    string         `json:"title" gorm:"not null"`
	Description              string         `json:"description" gorm:"type:text"`
	Content                  string         `json:"content" gorm:"type:text"`
	Status                   string         `json:"status" gorm:"default:'active'"`
	Tags                     pq.StringArray `json:"tags" gorm:"type:text[]" swaggertype:"array,string"`
	ViewCount                int            `json:"view_count" gorm:"default:0"`
	CurrentRevisionID        *uuid.UUID     `json:"current_revision_id,omitempty" gorm:"type:uuid;index"`
	CurrentConclusionEventID *uuid.UUID     `json:"current_conclusion_event_id,omitempty" gorm:"type:uuid;index"`
	ConclusionType           string         `json:"conclusion_type" gorm:"default:''"`
}

func (Debate) TableName() string { return "debates" }

const (
	DebateRelationSupport     = "support"
	DebateRelationOppose      = "oppose"
	DebateRelationActive      = "active"
	DebateRelationStale       = "stale"
	DebateRelationUnavailable = "unavailable"
	DebateStatusActive        = "active"
	DebateStatusArchived      = "archived"
	DebateVoteYes             = "yes"
	DebateVoteNo              = "no"
)

// DebateRelation projects a debate citation from the referenced source to the
// current target revision.
type DebateRelation struct {
	Base
	SourceDebateID          uuid.UUID `json:"source_debate_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_relation_pair,priority:1,where:deleted_at IS NULL"`
	SourceDebate            *Debate   `json:"source_debate,omitempty" gorm:"foreignKey:SourceDebateID"`
	TargetDebateID          uuid.UUID `json:"target_debate_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_relation_pair,priority:2,where:deleted_at IS NULL"`
	TargetDebate            *Debate   `json:"target_debate,omitempty" gorm:"foreignKey:TargetDebateID"`
	Stance                  string    `json:"stance" gorm:"type:varchar(16);not null"`
	TargetRevisionID        uuid.UUID `json:"target_revision_id" gorm:"type:uuid;not null;index"`
	SourceConclusionEventID uuid.UUID `json:"source_conclusion_event_id" gorm:"type:uuid;not null;index"`
	Status                  string    `json:"status" gorm:"type:varchar(16);not null;default:'active';index"`
}

func (DebateRelation) TableName() string { return "debate_relations" }

// DebateVote is one community yes/no vote on a debate.
type DebateVote struct {
	Base
	DebateID  uuid.UUID `json:"debate_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_vote_debate_user,priority:1,where:deleted_at IS NULL"`
	UserID    uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_vote_debate_user,priority:2,where:deleted_at IS NULL"`
	User      *User     `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Direction string    `json:"direction" gorm:"type:varchar(8);not null"`
}

func (DebateVote) TableName() string { return "debate_votes" }

// DebateConclusionEvent is an immutable snapshot created when the community
// first reaches a conclusion or later reverses it.
type DebateConclusionEvent struct {
	Base
	DebateID   uuid.UUID `json:"debate_id" gorm:"type:uuid;not null;index"`
	Direction  string    `json:"direction" gorm:"type:varchar(8);not null"`
	YesVotes   int       `json:"yes_votes" gorm:"not null"`
	NoVotes    int       `json:"no_votes" gorm:"not null"`
	TotalVotes int       `json:"total_votes" gorm:"not null"`
}

func (DebateConclusionEvent) TableName() string { return "debate_conclusion_events" }

// DebateRevisionReference stores every resource token resolved for one
// immutable debate revision. Occurrence keeps duplicate tokens distinct.
type DebateRevisionReference struct {
	Base
	DebateID           uuid.UUID  `json:"debate_id" gorm:"type:uuid;not null;index"`
	RevisionID         uuid.UUID  `json:"revision_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_revision_reference,priority:1"`
	ContentReferenceID uuid.UUID  `json:"content_reference_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_revision_content_reference,where:deleted_at IS NULL"`
	Raw                string     `json:"raw" gorm:"type:text;not null"`
	Kind               string     `json:"kind" gorm:"type:varchar(24);not null;uniqueIndex:idx_debate_revision_reference,priority:2"`
	ResourceID         uuid.UUID  `json:"resource_id" gorm:"type:uuid;not null;uniqueIndex:idx_debate_revision_reference,priority:3"`
	Title              string     `json:"title" gorm:"type:text;not null"`
	Qualifier          string     `json:"qualifier" gorm:"type:varchar(16);not null;default:'';uniqueIndex:idx_debate_revision_reference,priority:4"`
	Occurrence         int        `json:"occurrence" gorm:"not null;uniqueIndex:idx_debate_revision_reference,priority:5"`
	State              string     `json:"state" gorm:"type:varchar(16);not null;default:'active'"`
	RelationID         *uuid.UUID `json:"relation_id,omitempty" gorm:"type:uuid;index"`
}

func (DebateRevisionReference) TableName() string { return "debate_revision_references" }
