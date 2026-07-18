package notification

import (
	"time"

	"atoman/internal/model"
)

type ListQuery struct {
	Page     int    `json:"page" form:"page"`
	PageSize int    `json:"page_size" form:"page_size"`
	Type     string `json:"type" form:"type"`
}

type NotificationDTO struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Category   string                 `json:"category"`
	SourceType string                 `json:"source_type"`
	SourceID   string                 `json:"source_id"`
	Meta       model.NotificationMeta `json:"meta"`
	ReadAt     *time.Time             `json:"read_at,omitempty"`
	CreatedAt  time.Time              `json:"created_at"`
	Actor      *ActorDTO              `json:"actor,omitempty"`
}

type UnreadCountsDTO struct {
	Total int64            `json:"total" example:"8"`
	Items map[string]int64 `json:"items"`
}

type UnreadCountsResponse struct {
	Data UnreadCountsDTO `json:"data"`
}

type ActorDTO struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}
