package model

import "github.com/google/uuid"

// RecommendationFeedback stores a user's reversible opinion about a home recommendation.
type RecommendationFeedback struct {
	Base
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_recommendation_feedback_target,priority:1,where:deleted_at IS NULL"`
	TargetType string    `json:"target_type" gorm:"type:varchar(24);not null;uniqueIndex:idx_recommendation_feedback_target,priority:2,where:deleted_at IS NULL"`
	TargetID   uuid.UUID `json:"target_id" gorm:"type:uuid;not null;uniqueIndex:idx_recommendation_feedback_target,priority:3,where:deleted_at IS NULL"`
	Action     string    `json:"action" gorm:"type:varchar(24);not null;uniqueIndex:idx_recommendation_feedback_target,priority:4,where:deleted_at IS NULL"`
}

func (RecommendationFeedback) TableName() string { return "recommendation_feedback" }
