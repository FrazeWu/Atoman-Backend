package model

import (
	"time"

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
	DebateRelationRejected    = "rejected"
	DebateStatusActive        = "active"
	DebateStatusArchived      = "archived"
	DebateVoteYes             = "yes"
	DebateVoteNo              = "no"

	DebateEntryTypeClaim    = "claim"
	DebateEntryTypeEvidence = "evidence"
	DebateEntryTypeRebuttal = "rebuttal"

	DebateEntryStanceSupport = "support"
	DebateEntryStanceOppose  = "oppose"
	DebateEntryStanceNeutral = "neutral"

	DebateEntryStatusActive   = "active"
	DebateEntryStatusRejected = "rejected"

	DebateEvidenceRatingSufficient   = "sufficient"
	DebateEvidenceRatingPartial      = "partial"
	DebateEvidenceRatingInsufficient = "insufficient"

	DebateChallengeTargetRelation  = "relation"
	DebateChallengeTargetReference = "reference"
	DebateChallengeTargetEntry     = "entry"

	DebateChallengeReasonMismatch       = "mismatch"
	DebateChallengeReasonOutdated       = "outdated"
	DebateChallengeReasonLowReliability = "low_reliability"
	DebateChallengeReasonIrrelevant     = "irrelevant"

	DebateChallengeStatusOpen      = "open"
	DebateChallengeStatusUpheld    = "upheld"
	DebateChallengeStatusDismissed = "dismissed"

	DebateChallengeVoteUphold  = "uphold"
	DebateChallengeVoteDismiss = "dismiss"
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
	DebateID             uuid.UUID `json:"debate_id" gorm:"type:uuid;not null;index"`
	Direction            string    `json:"direction" gorm:"type:varchar(8);not null"`
	YesVotes             int       `json:"yes_votes" gorm:"not null"`
	NoVotes              int       `json:"no_votes" gorm:"not null"`
	TotalVotes           int       `json:"total_votes" gorm:"not null"`
	EvidenceSufficient   int       `json:"evidence_sufficient" gorm:"not null;default:0"`
	EvidencePartial      int       `json:"evidence_partial" gorm:"not null;default:0"`
	EvidenceInsufficient int       `json:"evidence_insufficient" gorm:"not null;default:0"`
	ActiveEvidenceCount  int       `json:"active_evidence_count" gorm:"not null;default:0"`
	EvidenceWeak         bool      `json:"evidence_weak" gorm:"not null;default:false"`
}

func (DebateConclusionEvent) TableName() string { return "debate_conclusion_events" }

// DebateEntry represents a structured claim, evidence, or rebuttal item attached to a debate.
type DebateEntry struct {
	Base
	DebateID            uuid.UUID  `json:"debate_id" gorm:"type:uuid;not null;index"`
	ParentID            *uuid.UUID `json:"parent_id,omitempty" gorm:"type:uuid;index"`
	UserID              uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	User                *User      `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	EntryType           string     `json:"entry_type" gorm:"type:varchar(16);not null"`
	Stance              string     `json:"stance" gorm:"type:varchar(16);not null"`
	Title               string     `json:"title" gorm:"type:varchar(200);not null;default:''"`
	Content             string     `json:"content" gorm:"type:text;not null"`
	Status              string     `json:"status" gorm:"type:varchar(16);not null;default:'active';index"`
	RejectedChallengeID *uuid.UUID `json:"rejected_challenge_id,omitempty" gorm:"type:uuid;index"`
}

func (DebateEntry) TableName() string { return "debate_entries" }

// DebateEvidenceVote records an independent evidence sufficiency assessment for a debate.
type DebateEvidenceVote struct {
	Base
	DebateID uuid.UUID `json:"debate_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_evidence_vote_debate_user,priority:1,where:deleted_at IS NULL"`
	UserID   uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_evidence_vote_debate_user,priority:2,where:deleted_at IS NULL"`
	User     *User     `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Rating   string    `json:"rating" gorm:"type:varchar(16);not null"`
}

func (DebateEvidenceVote) TableName() string { return "debate_evidence_votes" }

// DebateChallenge represents a community challenge against a debate relation, reference, or evidence entry.
type DebateChallenge struct {
	Base
	DebateID     uuid.UUID  `json:"debate_id" gorm:"type:uuid;not null;index"`
	TargetType   string     `json:"target_type" gorm:"type:varchar(16);not null"`
	TargetID     uuid.UUID  `json:"target_id" gorm:"type:uuid;not null;index"`
	CreatedBy    uuid.UUID  `json:"created_by" gorm:"type:uuid;not null;index"`
	Creator      *User      `json:"creator,omitempty" gorm:"foreignKey:CreatedBy;references:UUID"`
	ReasonCode   string     `json:"reason_code" gorm:"type:varchar(32);not null"`
	Rationale    string     `json:"rationale" gorm:"type:text;not null"`
	Status       string     `json:"status" gorm:"type:varchar(16);not null;default:'open';index"`
	UpholdVotes  int        `json:"uphold_votes" gorm:"not null;default:0"`
	DismissVotes int        `json:"dismiss_votes" gorm:"not null;default:0"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
}

func (DebateChallenge) TableName() string { return "debate_challenges" }

// DebateChallengeVote records a community vote on an open debate challenge.
type DebateChallengeVote struct {
	Base
	ChallengeID uuid.UUID `json:"challenge_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_challenge_vote_user,priority:1,where:deleted_at IS NULL"`
	UserID      uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_challenge_vote_user,priority:2,where:deleted_at IS NULL"`
	User        *User     `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Decision    string    `json:"decision" gorm:"type:varchar(16);not null"`
}

func (DebateChallengeVote) TableName() string { return "debate_challenge_votes" }

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
