package model

import (
	"time"

	"github.com/google/uuid"
)

const (
	MusicMatchUnmatched = "unmatched"
	MusicMatchMatched   = "matched"
	MusicMatchAmbiguous = "ambiguous"
	MusicMatchManual    = "manual"
)

// MusicMatchRecord stores provider identity and the result of a metadata match.
// Audio availability is tracked on Song because it is independent of metadata.
type MusicMatchRecord struct {
	Base
	EntityType     string     `json:"entity_type" gorm:"type:varchar(16);not null;uniqueIndex:idx_music_match_entity_provider,priority:1;index"`
	EntityID       uuid.UUID  `json:"entity_id" gorm:"type:uuid;not null;uniqueIndex:idx_music_match_entity_provider,priority:2;index"`
	Provider       string     `json:"provider" gorm:"type:varchar(32);not null;uniqueIndex:idx_music_match_entity_provider,priority:3"`
	ExternalID     string     `json:"external_id" gorm:"type:text;not null;default:''"`
	SourceURL      string     `json:"source_url" gorm:"type:text;not null;default:''"`
	Status         string     `json:"status" gorm:"type:varchar(16);not null;default:'unmatched';index"`
	Confidence     float64    `json:"confidence" gorm:"not null;default:0"`
	MatchedAt      *time.Time `json:"matched_at,omitempty"`
	UserOverridden bool       `json:"user_overridden" gorm:"not null;default:false;index"`
	MetadataJSON   string     `json:"metadata" gorm:"type:jsonb;not null;default:'{}'"`
}

func (MusicMatchRecord) TableName() string { return "music_match_records" }
