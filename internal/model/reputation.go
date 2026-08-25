package model

import (
	"time"

	"github.com/google/uuid"
)

const (
	ReputationRunRunning   = "running"
	ReputationRunPublished = "published"
	ReputationRunFailed    = "failed"

	ReputationQualityInitial       = "initial"
	ReputationQualityEvidenceBased = "evidence_based"

	ContributionEvidenceActive  = "active"
	ContributionEvidenceRevoked = "revoked"
)

// ReputationRun identifies one atomic shadow calculation cycle.
type ReputationRun struct {
	Base
	Status                  string     `json:"status" gorm:"type:varchar(16);not null;index"`
	QualityAlgorithmVersion string     `json:"quality_algorithm_version" gorm:"type:varchar(32);not null"`
	ContributionRuleVersion string     `json:"contribution_rule_version" gorm:"type:varchar(32);not null"`
	WeightAlgorithmVersion  string     `json:"weight_algorithm_version" gorm:"type:varchar(32);not null"`
	StartedAt               time.Time  `json:"started_at" gorm:"not null"`
	PublishedAt             *time.Time `json:"published_at,omitempty"`
	ErrorMessage            string     `json:"error_message,omitempty" gorm:"type:text"`
}

func (ReputationRun) TableName() string { return "reputation_runs" }

type BlogQualitySnapshot struct {
	Base
	RunID            uuid.UUID `json:"run_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_blog_quality_run_post,priority:1"`
	PostID           uuid.UUID `json:"post_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_blog_quality_run_post,priority:2"`
	ValidRatingCount int       `json:"valid_rating_count" gorm:"not null"`
	RawScore         float64   `json:"raw_score" gorm:"not null"`
	WeightedScore    *float64  `json:"weighted_score,omitempty"`
	WeightSum        float64   `json:"weight_sum" gorm:"not null"`
	QualityEstimate  float64   `json:"quality_estimate" gorm:"not null"`
	QualityActive    bool      `json:"quality_active" gorm:"not null;index"`
	PriorMean        float64   `json:"prior_mean" gorm:"not null"`
	PriorStrength    float64   `json:"prior_strength" gorm:"not null"`
}

func (BlogQualitySnapshot) TableName() string { return "blog_quality_snapshots" }

type UserReputationSnapshot struct {
	Base
	RunID               uuid.UUID `json:"run_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_user_reputation_run_user,priority:1"`
	UserID              uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_user_reputation_run_user,priority:2"`
	ContributionTotal   int       `json:"contribution_total" gorm:"not null"`
	QualityState        string    `json:"quality_state" gorm:"type:varchar(24);not null"`
	QualityEvidenceMass float64   `json:"quality_evidence_mass" gorm:"not null"`
	PortfolioQuality    float64   `json:"portfolio_quality" gorm:"not null"`
	StabilityRate       float64   `json:"stability_rate" gorm:"not null"`
	StabilityPenalty    float64   `json:"stability_penalty" gorm:"not null"`
	Quality             float64   `json:"quality" gorm:"not null;index"`
	EvaluationWeight    float64   `json:"evaluation_weight" gorm:"not null"`
}

func (UserReputationSnapshot) TableName() string { return "user_reputation_snapshots" }

type MusicContributionEvent struct {
	Base
	ActorUserID      uuid.UUID  `json:"actor_user_id" gorm:"type:uuid;not null;index"`
	OperationFamily  string     `json:"operation_family" gorm:"type:varchar(48);not null;index"`
	OperationType    string     `json:"operation_type" gorm:"type:varchar(64);not null"`
	TargetType       string     `json:"target_type" gorm:"type:varchar(24);not null;index"`
	TargetID         uuid.UUID  `json:"target_id" gorm:"type:uuid;not null;index"`
	NaturalDate      time.Time  `json:"natural_date" gorm:"type:date;not null;index"`
	Points           int        `json:"points" gorm:"not null"`
	Status           string     `json:"status" gorm:"type:varchar(16);not null;index"`
	EffectiveAt      time.Time  `json:"effective_at" gorm:"not null"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevocationReason string     `json:"revocation_reason,omitempty" gorm:"type:text"`
	MergeKey         string     `json:"merge_key" gorm:"type:varchar(192);not null;index"`
	SourceKind       string     `json:"source_kind" gorm:"type:varchar(24);not null;uniqueIndex:idx_music_contribution_event_source,priority:1"`
	SourceID         uuid.UUID  `json:"source_id" gorm:"type:uuid;not null;uniqueIndex:idx_music_contribution_event_source,priority:2"`
	RuleVersion      string     `json:"rule_version" gorm:"type:varchar(32);not null"`
	Metadata         []byte     `json:"metadata,omitempty" gorm:"type:jsonb"`
}

func (MusicContributionEvent) TableName() string { return "music_contribution_events" }

type MusicContributionEvidence struct {
	Base
	ActorUserID      uuid.UUID  `json:"actor_user_id" gorm:"type:uuid;not null;index"`
	OperationFamily  string     `json:"operation_family" gorm:"type:varchar(48);not null;index"`
	OperationType    string     `json:"operation_type" gorm:"type:varchar(64);not null"`
	TargetType       string     `json:"target_type" gorm:"type:varchar(24);not null;index"`
	TargetID         uuid.UUID  `json:"target_id" gorm:"type:uuid;not null;index"`
	NaturalDate      time.Time  `json:"natural_date" gorm:"type:date;not null;index"`
	Points           int        `json:"points" gorm:"not null"`
	Status           string     `json:"status" gorm:"type:varchar(16);not null;index"`
	EffectiveAt      time.Time  `json:"effective_at" gorm:"not null"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevocationReason string     `json:"revocation_reason,omitempty" gorm:"type:text"`
	MergeKey         string     `json:"merge_key" gorm:"type:varchar(192);not null;uniqueIndex"`
	SourceRevisionID *uuid.UUID `json:"source_revision_id,omitempty" gorm:"type:uuid;index"`
	SourceLyricID    *uuid.UUID `json:"source_lyric_id,omitempty" gorm:"type:uuid;index"`
	RuleVersion      string     `json:"rule_version" gorm:"type:varchar(32);not null"`
	Metadata         []byte     `json:"metadata,omitempty" gorm:"type:jsonb"`
}

func (MusicContributionEvidence) TableName() string { return "music_contribution_evidence" }
