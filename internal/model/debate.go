package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Debate represents a structured debate topic
type Debate struct {
	Base
	UserID            uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	User              *User          `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Title             string         `json:"title" gorm:"not null"`
	Description       string         `json:"description" gorm:"type:text"`
	Content           string         `json:"content" gorm:"type:text"`
	Status            string         `json:"status" gorm:"default:'open'"`
	Tags              pq.StringArray `json:"tags" gorm:"type:text[]"`
	ViewCount         int            `json:"view_count" gorm:"default:0"`
	ArgumentCount     int            `json:"argument_count" gorm:"default:0"`
	VoteCount         int            `json:"vote_count" gorm:"default:0"`
	ConclusionType    string         `json:"conclusion_type" gorm:"default:''"`
	ConclusionSummary string         `json:"conclusion_summary" gorm:"type:text"`
	ConcludeVoteCount int            `json:"conclude_vote_count" gorm:"default:0"`
	ConcludeThreshold int            `json:"conclude_threshold" gorm:"default:10"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	ConcludedAt       *time.Time     `json:"concluded_at,omitempty"`
}

func (Debate) TableName() string { return "debates" }

// ArgumentType represents the type of argument
type ArgumentType string

type ArgumentMention struct {
	UserID uuid.UUID `json:"user_id"`
	Start  int       `json:"start"`
	End    int       `json:"end"`
}

type ArgumentAttachment struct {
	ID          uuid.UUID `json:"id"`
	URL         string    `json:"url"`
	ContentType string    `json:"content_type"`
	Position    int       `json:"position"`
}

const (
	ArgumentTypeSupport  ArgumentType = "support"
	ArgumentTypeOppose   ArgumentType = "oppose"
	ArgumentTypeNeutral  ArgumentType = "neutral"
	ArgumentTypeEvidence ArgumentType = "evidence"
	ArgumentTypeQuestion ArgumentType = "question"
	ArgumentTypeCounter  ArgumentType = "counter"
)

// DebateArgumentDTO is the typed API representation backed by comment_entries.
type DebateArgumentDTO struct {
	Base
	DebateID          uuid.UUID           `json:"debate_id" gorm:"type:uuid;not null;index"`
	Debate            *Debate             `json:"debate,omitempty" gorm:"foreignKey:DebateID"`
	ParentID          *uuid.UUID          `json:"parent_id" gorm:"type:uuid;index"`
	Parent            *DebateArgumentDTO  `json:"parent,omitempty" gorm:"-"`
	UserID            uuid.UUID           `json:"user_id" gorm:"type:uuid;not null;index"`
	User              *User               `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Content           string              `json:"content" gorm:"type:text;not null"`
	ArgumentType      ArgumentType        `json:"argument_type" gorm:"type:varchar(20);not null"`
	VoteCount         int                 `json:"vote_count" gorm:"default:0"`
	References        []DebateArgumentDTO `json:"references,omitempty" gorm:"-"`
	ReferencedDebates []Debate            `json:"referenced_debates,omitempty" gorm:"many2many:argument_debate_refs;joinForeignKey:ArgumentID;JoinReferences:DebateID"`
	IsConcluded       bool                `json:"is_concluded" gorm:"default:false"`
	Conclusion        string              `json:"conclusion,omitempty" gorm:"type:text"`
	// Evidence source fields (only used when ArgumentType == "evidence")
	SourceURL     string `json:"source_url" gorm:"type:varchar(2048);default:''"`
	SourceTitle   string `json:"source_title" gorm:"type:varchar(512);default:''"`
	SourceExcerpt string `json:"source_excerpt" gorm:"type:text;default:''"`
	// Admin moderation
	IsFolded      bool                 `json:"is_folded" gorm:"default:false"`
	FoldNote      string               `json:"fold_note" gorm:"type:text;default:''"` // admin note for why folded
	Mentions      []ArgumentMention    `json:"mentions,omitempty" gorm:"-"`
	AttachmentIDs []uuid.UUID          `json:"attachment_ids,omitempty" gorm:"-"`
	Attachments   []ArgumentAttachment `json:"attachments,omitempty" gorm:"-"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
}

// DebateVote represents a real-name vote on an argument
type DebateVote struct {
	Base
	ArgumentID uuid.UUID          `json:"argument_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_vote_argument_user,priority:1"`
	Argument   *DebateArgumentDTO `json:"argument,omitempty" gorm:"-"`
	UserID     uuid.UUID          `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_vote_argument_user,priority:2"`
	User       *User              `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	VoteType   int                `json:"vote_type" gorm:"not null"`
	CreatedAt  time.Time          `json:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
}

func (DebateVote) TableName() string { return "debate_votes" }

// VoteHistory tracks vote changes for transparency
type VoteHistory struct {
	Base
	ArgumentID  uuid.UUID `json:"argument_id" gorm:"type:uuid;not null;index"`
	UserID      uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	OldVoteType int       `json:"old_vote_type"`
	NewVoteType int       `json:"new_vote_type"`
	CreatedAt   time.Time `json:"created_at"`
}

func (VoteHistory) TableName() string { return "vote_histories" }

// DebateConcludeVote tracks users who voted to conclude a debate
type DebateConcludeVote struct {
	Base
	DebateID  uuid.UUID `json:"debate_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_conclude_vote_debate_user,priority:1"`
	UserID    uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_debate_conclude_vote_debate_user,priority:2"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (DebateConcludeVote) TableName() string { return "debate_conclude_votes" }
