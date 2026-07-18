package studio

import (
	"strings"
	"time"

	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
)

type Module string

const (
	ModuleBlog    Module = "blog"
	ModulePodcast Module = "podcast"
	ModuleVideo   Module = "video"
)

func ParseModule(value string) (Module, error) {
	module := Module(strings.ToLower(strings.TrimSpace(value)))
	switch module {
	case ModuleBlog, ModulePodcast, ModuleVideo:
		return module, nil
	default:
		return "", apperr.BadRequest("studio.invalid_module", "module must be blog, podcast, or video")
	}
}

type ChannelSummary struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`
	CoverURL    string    `json:"cover_url"`
}

type StateResponse struct {
	CurrentChannel *ChannelSummary  `json:"current_channel"`
	Channels       []ChannelSummary `json:"channels"`
}

type PutStateInput struct {
	ChannelID uuid.UUID `json:"channel_id" binding:"required"`
}

type CreateChannelInput struct {
	Name        string `json:"name" binding:"required"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
}

type UpdateChannelInput struct {
	Name        *string `json:"name"`
	Slug        *string `json:"slug"`
	Description *string `json:"description"`
	CoverURL    *string `json:"cover_url"`
}

type CreateCollectionInput struct {
	ChannelID   uuid.UUID `json:"channel_id" binding:"required"`
	Name        string    `json:"name" binding:"required"`
	Description string    `json:"description"`
	CoverURL    string    `json:"cover_url"`
}

type UpdateCollectionInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	CoverURL    *string `json:"cover_url"`
}

type ContentQuery struct {
	ChannelID    uuid.UUID
	Search       string
	Status       string
	Visibility   string
	CollectionID uuid.UUID
	Page         int
	PageSize     int
}

type StudioCollectionSummary struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type StudioContentItem struct {
	ID               uuid.UUID                 `json:"id"`
	Module           Module                    `json:"module"`
	ChannelID        uuid.UUID                 `json:"channel_id"`
	Title            string                    `json:"title"`
	Summary          string                    `json:"summary"`
	CoverURL         string                    `json:"cover_url"`
	Status           string                    `json:"status"`
	Visibility       string                    `json:"visibility"`
	Collections      []StudioCollectionSummary `json:"collections"`
	DurationSec      int                       `json:"duration_sec,omitempty"`
	ViewCount        int64                     `json:"view_count"`
	ProcessingStatus string                    `json:"processing_status,omitempty"`
	PublishedAt      *time.Time                `json:"published_at,omitempty"`
	CreatedAt        time.Time                 `json:"created_at"`
	UpdatedAt        time.Time                 `json:"updated_at"`
}

type StudioContentIssue struct {
	Code  string `json:"code"`
	Count int64  `json:"count"`
}

type DashboardSection struct {
	Module  Module               `json:"module"`
	Metrics map[string]int64     `json:"metrics"`
	Recent  []StudioContentItem  `json:"recent"`
	Issues  []StudioContentIssue `json:"issues"`
	Error   string               `json:"error,omitempty"`
}

type DashboardResponse struct {
	ChannelSubscriberCount int64              `json:"channel_subscriber_count"`
	Sections               []DashboardSection `json:"sections"`
}
