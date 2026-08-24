package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ContentEntry is the canonical, channel-scoped record shared by publishable content.
// Type-specific data remains in the existing resource tables during the incremental migration.
type ContentEntry struct {
	Base
	AuthorID    *uuid.UUID `json:"author_id,omitempty" gorm:"type:uuid;index"`
	Author      *User      `json:"author,omitempty" gorm:"foreignKey:AuthorID;references:UUID"`
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

// ContentBlogExtension stores blog-specific data while ContentEntry owns the canonical identity and shared metadata.
type ContentBlogExtension struct {
	ContentID          uuid.UUID `json:"content_id" gorm:"type:uuid;primaryKey"`
	Content            string    `json:"content" gorm:"type:text;not null"`
	LanguageCode       string    `json:"language_code" gorm:"type:varchar(16);index"`
	Pinned             bool      `json:"pinned" gorm:"not null;default:false"`
	ViewCount          int64     `json:"view_count" gorm:"not null;default:0"`
	CollectionConflict bool      `json:"collection_conflict" gorm:"not null;default:false;index"`
}

func (ContentBlogExtension) TableName() string { return "content_blog_extensions" }

type ContentBlogVersion struct {
	Base
	ContentID    uuid.UUID  `json:"content_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_content_blog_version,priority:1"`
	Version      int        `json:"version" gorm:"not null;uniqueIndex:idx_content_blog_version,priority:2"`
	EditorID     uuid.UUID  `json:"editor_id" gorm:"type:uuid;not null;index"`
	Title        string     `json:"title" gorm:"not null"`
	Content      string     `json:"content" gorm:"type:text;not null"`
	Summary      string     `json:"summary" gorm:"type:text"`
	CoverURL     string     `json:"cover_url" gorm:"type:text"`
	LanguageCode string     `json:"language_code" gorm:"type:varchar(16);index"`
	Pinned       bool       `json:"pinned" gorm:"not null;default:false"`
	Visibility   string     `json:"visibility" gorm:"not null"`
	CollectionID uuid.UUID  `json:"collection_id" gorm:"type:uuid;not null;index"`
	PublishedAt  *time.Time `json:"published_at,omitempty"`
}

func (ContentBlogVersion) TableName() string { return "content_blog_versions" }

type ContentBlogDraft struct {
	Base
	UserID       uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_content_blog_drafts_user_context,priority:1"`
	ContentID    *uuid.UUID `json:"content_id,omitempty" gorm:"type:uuid;index"`
	ContextKey   string     `json:"context_key" gorm:"not null;uniqueIndex:idx_content_blog_drafts_user_context,priority:2"`
	Title        string     `json:"title"`
	Content      string     `json:"content" gorm:"type:text"`
	Summary      string     `json:"summary" gorm:"type:text"`
	CoverURL     string     `json:"cover_url" gorm:"type:text"`
	Visibility   string     `json:"visibility" gorm:"not null;default:'public'"`
	ChannelID    *uuid.UUID `json:"channel_id,omitempty" gorm:"type:uuid;index"`
	CollectionID *uuid.UUID `json:"collection_id,omitempty" gorm:"type:uuid;index"`
}

func (ContentBlogDraft) TableName() string { return "content_blog_drafts" }

type ContentEpisodeExtension struct {
	ContentID          uuid.UUID `json:"content_id" gorm:"type:uuid;primaryKey"`
	EpisodeID          uuid.UUID `json:"episode_id" gorm:"type:uuid;not null;uniqueIndex"` // legacy resource identity during API transition
	LegacyPostID       uuid.UUID `json:"legacy_post_id" gorm:"type:uuid"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	AudioURL           string    `json:"audio_url" gorm:"type:text"`
	DurationSec        int       `json:"duration_sec" gorm:"not null;default:0"`
	EpisodeCoverURL    string    `json:"episode_cover_url" gorm:"type:text"`
	SeasonNumber       int       `json:"season_number" gorm:"not null;default:1"`
	EpisodeNumber      int       `json:"episode_number" gorm:"not null;default:0"`
	Shownotes          string    `json:"shownotes" gorm:"type:text"`
	ViewCount          int64     `json:"view_count" gorm:"not null;default:0"`
	CollectionConflict bool      `json:"collection_conflict" gorm:"not null;default:false;index"`
}

func (ContentEpisodeExtension) TableName() string { return "content_episode_extensions" }

type ContentVideoExtension struct {
	ContentID          uuid.UUID       `json:"content_id" gorm:"type:uuid;primaryKey"`
	VideoID            uuid.UUID       `json:"video_id" gorm:"type:uuid;not null;uniqueIndex"` // legacy resource identity during API transition
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	StorageType        string          `json:"storage_type" gorm:"default:'external'"`
	VideoURL           string          `json:"video_url" gorm:"type:text"`
	ThumbnailURL       string          `json:"thumbnail_url" gorm:"type:text"`
	DurationSec        int             `json:"duration_sec" gorm:"not null;default:0"`
	ProcessingStatus   string          `json:"processing_status" gorm:"not null;default:'none'"`
	ProcessingError    string          `json:"processing_error" gorm:"type:text"`
	PreviewThumbnails  json.RawMessage `json:"preview_thumbnails" gorm:"type:jsonb"`
	ViewCount          int             `json:"view_count" gorm:"not null;default:0"`
	CollectionConflict bool            `json:"collection_conflict" gorm:"not null;default:false;index"`
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
