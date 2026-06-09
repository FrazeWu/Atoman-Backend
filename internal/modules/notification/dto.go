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
	SourceType string                 `json:"source_type"`
	SourceID   string                 `json:"source_id"`
	Meta       model.NotificationMeta `json:"meta"`
	ReadAt     *time.Time             `json:"read_at,omitempty"`
	CreatedAt  time.Time              `json:"created_at"`
	Actor      *ActorDTO              `json:"actor,omitempty"`
}

type ActorDTO struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}
