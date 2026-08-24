package model

import (
	"github.com/google/uuid"
)

// MusicRecommendationEvent records how a signed-in user consumes a recommendation.
// Entity rows are intentionally not foreign keys so analytics survive catalog cleanup.
type MusicRecommendationEvent struct {
	Base
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index:idx_music_recommendation_user"`
	RequestID  uuid.UUID `json:"request_id" gorm:"type:uuid;not null;index"`
	Surface    string    `json:"surface" gorm:"type:varchar(64);not null"`
	Event      string    `json:"event" gorm:"type:varchar(32);not null;index:idx_music_recommendation_event"`
	EntityType string    `json:"entity_type" gorm:"type:varchar(32);not null;index"`
	EntityID   uuid.UUID `json:"entity_id" gorm:"type:uuid;not null;index"`
	Position   int       `json:"position" gorm:"not null;default:-1"`
	Reason     string    `json:"reason,omitempty" gorm:"type:text"`
}

func (MusicRecommendationEvent) TableName() string {
	return "music_recommendation_events"
}
