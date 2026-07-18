package model

import "github.com/google/uuid"

type OnboardingFeedRecommendation struct {
	Base
	FeedSourceID uuid.UUID   `json:"feed_source_id" gorm:"type:uuid;not null;uniqueIndex"`
	FeedSource   *FeedSource `json:"feed_source,omitempty" gorm:"foreignKey:FeedSourceID"`
	Enabled      bool        `json:"enabled" gorm:"not null;default:true;index"`
	SortOrder    int         `json:"sort_order" gorm:"not null;default:0;index"`
}

func (OnboardingFeedRecommendation) TableName() string { return "onboarding_feed_recommendations" }
