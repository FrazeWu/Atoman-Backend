package studio

import (
	"strings"

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
