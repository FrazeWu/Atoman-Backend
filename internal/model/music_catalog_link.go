package model

import (
	"time"

	"github.com/google/uuid"
)

// MusicCatalogLink maps a provider identity to an Atoman music entity.
// It stores metadata only; licensed media URLs belong to their provider.
type MusicCatalogLink struct {
	Base
	Provider     string    `json:"provider" gorm:"not null;uniqueIndex:idx_music_catalog_link_identity,priority:1"`
	EntityType   string    `json:"entity_type" gorm:"not null;uniqueIndex:idx_music_catalog_link_identity,priority:2;index:idx_music_catalog_link_entity,priority:1"`
	ExternalID   string    `json:"external_id" gorm:"not null;uniqueIndex:idx_music_catalog_link_identity,priority:3"`
	EntityID     uuid.UUID `json:"entity_id" gorm:"type:uuid;not null;index:idx_music_catalog_link_entity,priority:2"`
	Storefront   string    `json:"storefront" gorm:"not null"`
	URL          string    `json:"url" gorm:"type:text;not null"`
	ChartRank    int       `json:"chart_rank" gorm:"not null;default:0"`
	MetadataJSON string    `json:"metadata" gorm:"type:jsonb;not null;default:'{}'"`
	LastSyncedAt time.Time `json:"last_synced_at" gorm:"not null"`
}

func (MusicCatalogLink) TableName() string { return "music_catalog_links" }
