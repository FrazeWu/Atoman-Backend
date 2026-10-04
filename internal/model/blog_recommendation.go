package model

import (
	"time"

	"github.com/google/uuid"
)

// BlogRecommendationPreference controls account-level personalization for blog discovery.
type BlogRecommendationPreference struct {
	UserID    uuid.UUID `json:"user_id" gorm:"type:uuid;primaryKey"`
	Enabled   bool      `json:"enabled" gorm:"not null;default:true"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (BlogRecommendationPreference) TableName() string { return "blog_recommendation_preferences" }
