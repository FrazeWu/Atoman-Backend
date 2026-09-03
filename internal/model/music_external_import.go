package model

import (
	"time"

	"github.com/google/uuid"
)

// MusicExternalImport keeps the rights and identity snapshot for catalog media
// imported from an external open-content provider.
type MusicExternalImport struct {
	Base
	Provider          string     `json:"provider" gorm:"not null;uniqueIndex:idx_music_external_import_provider_id,priority:1"`
	ExternalID        string     `json:"external_id" gorm:"not null;uniqueIndex:idx_music_external_import_provider_id,priority:2"`
	CreatorKey        string     `json:"creator_key" gorm:"not null;default:'';index"`
	SourceURL         string     `json:"source_url" gorm:"type:text;not null"`
	LicenseCode       string     `json:"license_code" gorm:"not null"`
	LicenseURL        string     `json:"license_url" gorm:"type:text;not null"`
	AttributionText   string     `json:"attribution_text" gorm:"type:text;not null"`
	LicenseObservedAt time.Time  `json:"license_observed_at" gorm:"not null"`
	Popularity        int64      `json:"popularity" gorm:"not null;default:0;index"`
	FileHashesJSON    string     `json:"file_hashes" gorm:"type:jsonb;not null;default:'{}'"`
	MetadataJSON      string     `json:"metadata" gorm:"type:jsonb;not null;default:'{}'"`
	ImportSessionID   *uuid.UUID `json:"import_session_id,omitempty" gorm:"type:uuid;uniqueIndex"`
	ArtistID          *uuid.UUID `json:"artist_id,omitempty" gorm:"type:uuid;index"`
	AlbumID           *uuid.UUID `json:"album_id,omitempty" gorm:"type:uuid;index"`
	SongID            *uuid.UUID `json:"song_id,omitempty" gorm:"type:uuid;index"`
}

func (MusicExternalImport) TableName() string { return "music_external_imports" }
