package feed

import (
	"atoman/internal/model"

	"github.com/google/uuid"
)

type SubscriptionInput struct {
	TargetType string     `json:"target_type" binding:"required,oneof=internal_user internal_channel internal_collection external_rss"`
	TargetID   *uuid.UUID `json:"target_id"`
	RssURL     string     `json:"rss_url"`
	Title      string     `json:"title"`
}

type ErrorResponse struct {
	Error string `json:"error" example:"invalid request"`
}

type MessageResponse struct {
	Message string `json:"message" example:"ok"`
}

type SubscriptionResponse struct {
	Data    model.Subscription `json:"data"`
	Message string             `json:"message" example:"ok"`
}

type SubscriptionListResponse struct {
	Data    []model.Subscription `json:"data"`
	Message string               `json:"message" example:"ok"`
}

type TimelineResponse struct {
	Data    []TimelineItem `json:"data"`
	Total   int            `json:"total" example:"20"`
	Page    int            `json:"page" example:"1"`
	Limit   int            `json:"limit" example:"20"`
	Message string         `json:"message" example:"ok"`
}

type FeedStatsResponse struct {
	Data    FeedStatsData `json:"data"`
	Message string        `json:"message" example:"ok"`
}

type SubscriptionGroupResponse struct {
	Data    model.SubscriptionGroup `json:"data"`
	Message string                  `json:"message" example:"ok"`
}

type SubscriptionGroupListResponse struct {
	Data    []model.SubscriptionGroup `json:"data"`
	Message string                    `json:"message" example:"ok"`
}

type OPMLImportResponse struct {
	Message  string `json:"message" example:"OPML import completed"`
	Imported int    `json:"imported" example:"12"`
	Reused   int    `json:"reused" example:"3"`
	Failed   int    `json:"failed" example:"1"`
}

type StarToggleInput struct {
	FeedItemID string `json:"feed_item_id" format:"uuid" binding:"required" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
}

type StarToggleResponse struct {
	Starred bool   `json:"starred" example:"true"`
	Message string `json:"message" example:"Item starred"`
}

type StarredFeedItem struct {
	ID             string `json:"id" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Title          string `json:"title" example:"An interesting post"`
	Link           string `json:"link" example:"https://example.com/post"`
	Summary        string `json:"summary" example:"Summary text"`
	Author         string `json:"author" example:"Fafa"`
	PublishedAt    string `json:"published_at" format:"date-time" example:"2026-05-25T11:00:00Z"`
	SourceTitle    string `json:"source_title" example:"My Feed"`
	SourceSiteURL  string `json:"source_site_url" example:"https://example.com/feed.xml"`
	SourceImageURL string `json:"source_image_url" example:"https://example.com/logo.png"`
}

type StarredItemsResponse struct {
	Items []StarredFeedItem `json:"items"`
	Page  int               `json:"page" example:"1"`
	Total int               `json:"total" example:"20"`
}

type SaveToggleResponse struct {
	Saved bool `json:"saved" example:"true"`
}

type ReadingListResponse struct {
	Items []model.ReadingListItem `json:"items"`
	Page  int                     `json:"page" example:"1"`
	Total int64                   `json:"total" example:"12"`
}

type FeedHealthCheckResponse struct {
	SubscriptionID string `json:"subscription_id" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	HealthStatus   string `json:"health_status" example:"healthy"`
	ErrorMessage   string `json:"error_message,omitempty" example:""`
	LastChecked    string `json:"last_checked,omitempty" format:"date-time" example:"2026-05-25T11:00:00Z"`
	Skipped        bool   `json:"skipped,omitempty" example:"true"`
	Reason         string `json:"reason,omitempty" example:"internal subscription has no external URL"`
}

type FeedHealthCheckListResponse struct {
	CheckedCount int                       `json:"checked_count" example:"5"`
	Results      []FeedHealthCheckResponse `json:"results"`
}

type FeedItemResponse struct {
	Data FeedItemDetailResponse `json:"data"`
}

type SearchSubscriptionsResponse struct {
	Data  []model.Subscription `json:"data"`
	Count int                  `json:"count" example:"5"`
}

type SubscriptionActionResponse struct {
	Message      string             `json:"message" example:"Subscribed successfully"`
	Subscription model.Subscription `json:"subscription"`
}

type SubscriptionStatusResponse struct {
	Subscribed   bool               `json:"subscribed" example:"true"`
	Subscription model.Subscription `json:"subscription,omitempty"`
}
