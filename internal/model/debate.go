package model

import (
	"time"

	"github.com/google/uuid"
)

// Debate represents a structured debate topic
type Debate struct {
	Base
	UserID            uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	User              *User      `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Title             string     `json:"title" gorm:"not null"`
	Description       string     `json:"description" gorm:"type:text"`
	Content           string     `json:"content" gorm:"type:text"`
	Status            string     `json:"status" gorm:"default:'open'"`
	Tags              []string   `json:"tags" gorm:"type:text[]"`
	ViewCount         int        `json:"view_count" gorm:"default:0"`
	ArgumentCount     int        `json:"argument_count" gorm:"default:0"`
	VoteCount         int        `json:"vote_count" gorm:"default:0"`
	ConclusionType    string     `json:"conclusion_type" gorm:"default:''"`
	ConclusionSummary string     `json:"conclusion_summary" gorm:"type:text"`
	ConcludeVoteCount int        `json:"conclude_vote_count" gorm:"default:0"`
	ConcludeThreshold int        `json:"conclude_threshold" gorm:"default:10"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	ConcludedAt       *time.Time `json:"concluded_at,omitempty"`
}

func (Debate) TableName() string { return "debates" }

// ArgumentType represents the type of argument
type ArgumentType string

const (
	ArgumentTypeSupport  ArgumentType = "support"
	ArgumentTypeOppose   ArgumentType = "oppose"
	ArgumentTypeNeutral  ArgumentType = "neutral"
	ArgumentTypeEvidence ArgumentType = "evidence"
	ArgumentTypeQuestion ArgumentType = "question"
	ArgumentTypeCounter  ArgumentType = "counter"
)

// Argument represents an argument within a debate.
// ParentID stores the quoted argument rather than a tree parent.
type Argument struct {
	Base
	DebateID          uuid.UUID    `json:"debate_id" gorm:"type:uuid;not null;index"`
	Debate            *Debate      `json:"debate,omitempty" gorm:"foreignKey:DebateID"`
	ParentID          *uuid.UUID   `json:"parent_id" gorm:"type:uuid;index"`
	Parent            *Argument    `json:"parent,omitempty" gorm:"foreignKey:ParentID"`
	UserID            uuid.UUID    `json:"user_id" gorm:"type:uuid;not null;index"`
	User              *User        `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Content           string       `json:"content" gorm:"type:text;not null"`
	ArgumentType      ArgumentType `json:"argument_type" gorm:"type:varchar(20);not null"`
	VoteCount         int          `json:"vote_count" gorm:"default:0"`
	References        []Argument   `json:"references,omitempty" gorm:"many2many:argument_references;joinForeignKey:ArgumentID;JoinReferences:ReferenceID"`
	ReferencedDebates []Debate     `json:"referenced_debates,omitempty" gorm:"many2many:argument_debate_refs;joinForeignKey:ArgumentID;JoinReferences:DebateID"`
	IsConcluded       bool         `json:"is_concluded" gorm:"default:false"`
	Conclusion        string       `json:"conclusion,omitempty" gorm:"type:text"`
	// Evidence source fields (only used when ArgumentType == "evidence")
	SourceURL     string `json:"source_url" gorm:"type:varchar(2048);default:''"`
	SourceTitle   string `json:"source_title" gorm:"type:varchar(512);default:''"`
	SourceExcerpt string `json:"source_excerpt" gorm:"type:text;default:''"`
	// Admin moderation
	IsFolded  bool      `json:"is_folded" gorm:"default:false"`
	FoldNote  string    `json:"fold_note" gorm:"type:text;default:''"` // admin note for why folded
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Argument) TableName() string { return "arguments" }

// DebateVote represents a real-name vote on an argument
type DebateVote struct {
	Base
	ArgumentID uuid.UUID `json:"argument_id" gorm:"type:uuid;not null;index"`
	Argument   *Argument `json:"argument,omitempty" gorm:"foreignKey:ArgumentID"`
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	User       *User     `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	VoteType   int       `json:"vote_type" gorm:"not null"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
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
	DebateID  uuid.UUID `json:"debate_id" gorm:"type:uuid;not null;index"`
	UserID    uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (DebateConcludeVote) TableName() string { return "debate_conclude_votes" }
