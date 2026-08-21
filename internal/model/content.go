package model

import (
	"time"

	"github.com/google/uuid"
)

// ContentEntry is the canonical, channel-scoped record shared by publishable content.
// Type-specific data remains in the existing resource tables during the incremental migration.
type ContentEntry struct {
	Base
	ChannelID   uuid.UUID  `json:"channel_id" gorm:"type:uuid;not null;index"`
	Channel     *Channel   `json:"channel,omitempty" gorm:"foreignKey:ChannelID"`
	Kind        string     `json:"kind" gorm:"type:varchar(16);not null;index"` // blog | podcast | video
	Title       string     `json:"title" gorm:"not null"`
	Summary     string     `json:"summary" gorm:"type:text"`
	CoverURL    string     `json:"cover_url" gorm:"type:text"`
	Status      string     `json:"status" gorm:"not null;index"`
	Visibility  string     `json:"visibility" gorm:"not null;index"`
	PublishedAt *time.Time `json:"published_at,omitempty" gorm:"index"`
	ScheduledAt *time.Time `json:"scheduled_at,omitempty" gorm:"index"`
}

func (ContentEntry) TableName() string { return "content_entries" }

type ContentPostExtension struct {
	ContentID uuid.UUID `json:"content_id" gorm:"type:uuid;primaryKey"`
	PostID    uuid.UUID `json:"post_id" gorm:"type:uuid;not null;uniqueIndex"`
}

func (ContentPostExtension) TableName() string { return "content_post_extensions" }

type ContentEpisodeExtension struct {
	ContentID uuid.UUID `json:"content_id" gorm:"type:uuid;primaryKey"`
	EpisodeID uuid.UUID `json:"episode_id" gorm:"type:uuid;not null;uniqueIndex"`
}

func (ContentEpisodeExtension) TableName() string { return "content_episode_extensions" }

type ContentVideoExtension struct {
	ContentID uuid.UUID `json:"content_id" gorm:"type:uuid;primaryKey"`
	VideoID   uuid.UUID `json:"video_id" gorm:"type:uuid;not null;uniqueIndex"`
}

func (ContentVideoExtension) TableName() string { return "content_video_extensions" }

// ContentCollection is the mixed-content replacement for the legacy type-scoped Collection.
type ContentCollection struct {
	Base
	ChannelID   uuid.UUID  `json:"channel_id" gorm:"type:uuid;not null;index"`
	Channel     *Channel   `json:"channel,omitempty" gorm:"foreignKey:ChannelID"`
	CreatedBy   *uuid.UUID `json:"created_by,omitempty" gorm:"type:uuid;index"`
	Name        string     `json:"name" gorm:"not null"`
	Description string     `json:"description" gorm:"type:text"`
	CoverURL    string     `json:"cover_url" gorm:"type:text"`
	IsDefault   bool       `json:"is_default" gorm:"default:false;index"`
}

func (ContentCollection) TableName() string { return "content_collections" }

type ContentCollectionMembership struct {
	ContentID    uuid.UUID `json:"content_id" gorm:"type:uuid;primaryKey"`
	CollectionID uuid.UUID `json:"collection_id" gorm:"type:uuid;primaryKey"`
	Position     int       `json:"position" gorm:"not null;default:0"`
}

func (ContentCollectionMembership) TableName() string { return "content_collection_memberships" }

// LegacyCollectionMapping makes the backfill resumable while legacy endpoints are still live.
type LegacyCollectionMapping struct {
	LegacyCollectionID  uuid.UUID `json:"legacy_collection_id" gorm:"type:uuid;primaryKey"`
	ContentCollectionID uuid.UUID `json:"content_collection_id" gorm:"type:uuid;not null;index"`
}

func (LegacyCollectionMapping) TableName() string { return "legacy_collection_mappings" }
