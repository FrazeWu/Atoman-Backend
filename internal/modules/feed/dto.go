package feed

import (
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
)

type FeedQuery struct {
	Page           int       `json:"page" form:"page"`
	PageSize       int       `json:"page_size" form:"page_size"`
	ContentType    string    `json:"content_type" form:"content_type"`
	SourceType     string    `json:"source_type" form:"source_type"`
	SourceID       uuid.UUID `json:"source_id" form:"source_id"`
	GroupID        uuid.UUID `json:"group_id" form:"group_id"`
	IsRead         *bool     `json:"is_read" form:"is_read"`
	HideDuplicates bool      `json:"hide_duplicates" form:"hide_duplicates"`
	Sort           string    `json:"sort" form:"sort"`
	Search         string    `json:"q" form:"q"`
}

type TimelineItemDTO struct {
	Type        string          `json:"type"`
	Post        *model.Post     `json:"post,omitempty"`
	FeedItem    *model.FeedItem `json:"feed_item,omitempty"`
	PublishedAt time.Time       `json:"published_at"`
	IsRead      bool            `json:"is_read"`
}

type ToggleStateDTO struct {
	Active bool `json:"active"`
}

type RecommendationItemDTO struct {
	ID                   string                     `json:"id"`
	Title                string                     `json:"title"`
	Summary              string                     `json:"summary"`
	Description          string                     `json:"description"`
	ContentType          string                     `json:"content_type"`
	ImageURL             string                     `json:"image_url"`
	TargetPath           string                     `json:"target_path"`
	ScoreLabel           string                     `json:"score_label"`
	PlayCount            int64                      `json:"play_count"`
	BookmarkCount        int64                      `json:"bookmark_count"`
	ReadCount            int64                      `json:"read_count"`
	UpdateFrequencyLabel string                     `json:"update_frequency_label"`
	LastPublishedAt      *time.Time                 `json:"last_published_at,omitempty"`
	RecentItems          []RecommendationPreviewDTO `json:"recent_items,omitempty"`
}

type RecommendationThemeDTO struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type RecommendationPreviewDTO struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
