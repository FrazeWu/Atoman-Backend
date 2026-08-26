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
	Issue        string
	Page         int
	PageSize     int
}

type StudioCollectionSummary struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type StudioContentItem struct {
	ID                 uuid.UUID                 `json:"id"`
	Module             Module                    `json:"module"`
	ChannelID          uuid.UUID                 `json:"channel_id"`
	Title              string                    `json:"title"`
	Summary            string                    `json:"summary"`
	CoverURL           string                    `json:"cover_url"`
	Status             string                    `json:"status"`
	Visibility         string                    `json:"visibility"`
	Collection         *StudioCollectionSummary  `json:"collection,omitempty"`
	Collections        []StudioCollectionSummary `json:"collections"`
	CollectionConflict bool                      `json:"collection_conflict"`
	DurationSec        int                       `json:"duration_sec,omitempty"`
	ViewCount          int64                     `json:"view_count"`
	Metrics            map[string]int64          `json:"metrics"`
	ProcessingStatus   string                    `json:"processing_status,omitempty"`
	PublishedAt        *time.Time                `json:"published_at,omitempty"`
	ScheduledAt        *time.Time                `json:"scheduled_at,omitempty"`
	CreatedAt          time.Time                 `json:"created_at"`
	UpdatedAt          time.Time                 `json:"updated_at"`
}

type StudioCollectionContentItem struct {
	ContentID uuid.UUID `json:"content_id"`
	ID        uuid.UUID `json:"id"`
	Module    Module    `json:"module"`
	Title     string    `json:"title"`
	CoverURL  string    `json:"cover_url"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
	Position  int       `json:"position"`
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

type SettingsInput struct {
	ChannelID            uuid.UUID  `json:"channel_id"`
	DefaultCollectionID  *uuid.UUID `json:"default_collection_id"`
	DefaultVisibility    *string    `json:"default_visibility"`
	DefaultPublishStatus *string    `json:"default_publish_status"`
	AutoplayEnabled      *bool      `json:"autoplay_enabled"`
}

type SettingsResponse struct {
	ChannelID            uuid.UUID  `json:"channel_id"`
	Module               Module     `json:"module"`
	DefaultCollectionID  *uuid.UUID `json:"default_collection_id"`
	DefaultVisibility    string     `json:"default_visibility"`
	DefaultPublishStatus string     `json:"default_publish_status"`
	AutoplayEnabled      bool       `json:"autoplay_enabled"`
}

type InteractionQuery struct {
	ChannelID uuid.UUID
	ContentID uuid.UUID
	Search    string
	Unreplied bool
	Anchored  bool
	Handled   *bool
	Priority  string
	Page      int
	PageSize  int
}

type StudioInteractionAuthor struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url"`
}

type StudioTimeAnchor struct {
	Start   int `json:"start"`
	End     int `json:"end"`
	Seconds int `json:"seconds"`
}

type StudioInteractionItem struct {
	ID           uuid.UUID               `json:"id"`
	ContentID    uuid.UUID               `json:"content_id"`
	ContentTitle string                  `json:"content_title"`
	TargetKind   string                  `json:"target_kind"`
	Author       StudioInteractionAuthor `json:"author"`
	Content      string                  `json:"content"`
	ReplyCount   int                     `json:"reply_count"`
	Replied      bool                    `json:"replied"`
	Pinned       bool                    `json:"pinned"`
	Handled      bool                    `json:"handled"`
	Priority     string                  `json:"priority"`
	TimeAnchors  []StudioTimeAnchor      `json:"time_anchors"`
	CreatedAt    time.Time               `json:"created_at"`
}

type AnalyticsQuery struct {
	ChannelID    uuid.UUID
	CollectionID uuid.UUID
	ContentID    uuid.UUID
	Range        int
}

type AnalyticsPoint struct {
	Date    string           `json:"date"`
	Metrics map[string]int64 `json:"metrics"`
}

type AnalyticsContentMetric struct {
	ID      uuid.UUID        `json:"id"`
	Title   string           `json:"title"`
	Metrics map[string]int64 `json:"metrics"`
}

type AnalyticsSourceMetric struct {
	Source string `json:"source"`
	Count  int64  `json:"count"`
}

type AnalyticsRetention struct {
	Consumers          int64 `json:"consumers"`
	ReturningConsumers int64 `json:"returning_consumers"`
	Rate               int64 `json:"rate"`
}

type AnalyticsPeriod struct {
	From   time.Time        `json:"from"`
	To     time.Time        `json:"to"`
	Totals map[string]int64 `json:"totals"`
}

type AnalyticsResponse struct {
	Range          int                      `json:"range"`
	From           time.Time                `json:"from"`
	To             time.Time                `json:"to"`
	Totals         map[string]int64         `json:"totals"`
	PreviousPeriod AnalyticsPeriod          `json:"previous_period"`
	Trend          []AnalyticsPoint         `json:"trend"`
	Top            []AnalyticsContentMetric `json:"top"`
	Sources        []AnalyticsSourceMetric  `json:"sources"`
	Retention      AnalyticsRetention       `json:"retention"`
}

type ShareResponse struct {
	Path string `json:"path"`
}

type StudioCollectionContentCandidate struct {
	ContentID             uuid.UUID  `json:"content_id"`
	ID                    uuid.UUID  `json:"id"`
	Module                Module     `json:"module"`
	Title                 string     `json:"title"`
	CoverURL              string     `json:"cover_url"`
	Status                string     `json:"status"`
	CurrentCollectionID   *uuid.UUID `json:"current_collection_id,omitempty"`
	CurrentCollectionName string     `json:"current_collection_name,omitempty"`
}

type StudioPreflightIssue struct {
	Code string `json:"code"`
}

type StudioCalendarQuery struct {
	ChannelID uuid.UUID
	From      time.Time
	To        time.Time
}

type StudioCalendarItem struct {
	ContentID   uuid.UUID              `json:"content_id"`
	ID          uuid.UUID              `json:"id"`
	Module      Module                 `json:"module"`
	Title       string                 `json:"title"`
	ScheduledAt time.Time              `json:"scheduled_at"`
	Preflight   []StudioPreflightIssue `json:"preflight"`
}

type UpdateInteractionStateInput struct {
	Handled  *bool   `json:"handled"`
	Priority *string `json:"priority"`
}

type BatchInteractionStateInput struct {
	CommentIDs []uuid.UUID `json:"comment_ids" binding:"required"`
	Handled    bool        `json:"handled"`
}

type StudioReplyTemplateInput struct {
	ChannelID uuid.UUID `json:"channel_id" binding:"required"`
	Name      string    `json:"name" binding:"required"`
	Content   string    `json:"content" binding:"required"`
}

type StudioReplyTemplate struct {
	ID        uuid.UUID `json:"id"`
	ChannelID uuid.UUID `json:"channel_id"`
	Name      string    `json:"name"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type StudioGoalMetricOption struct {
	Module Module `json:"module"`
	Metric string `json:"metric"`
	Label  string `json:"label"`
}

type StudioGoalAction struct {
	ID            uuid.UUID  `json:"id"`
	GoalID        uuid.UUID  `json:"goal_id"`
	Title         string     `json:"title"`
	Status        string     `json:"status"`
	DueDate       *time.Time `json:"due_date,omitempty"`
	ContentID     *uuid.UUID `json:"content_id,omitempty"`
	ContentModule Module     `json:"content_module,omitempty"`
}

type StudioGoal struct {
	ID            uuid.UUID          `json:"id"`
	CycleID       uuid.UUID          `json:"cycle_id"`
	Name          string             `json:"name"`
	Module        Module             `json:"module"`
	Metric        string             `json:"metric"`
	BaselineValue int64              `json:"baseline_value"`
	TargetValue   int64              `json:"target_value"`
	CurrentValue  int64              `json:"current_value"`
	ActualValue   *int64             `json:"actual_value,omitempty"`
	Progress      int                `json:"progress"`
	Actions       []StudioGoalAction `json:"actions"`
}

type StudioGoalReview struct {
	ID         uuid.UUID `json:"id"`
	Result     string    `json:"result"`
	Learning   string    `json:"learning"`
	NextAction string    `json:"next_action"`
	CreatedAt  time.Time `json:"created_at"`
}

type StudioGoalCycle struct {
	ID          uuid.UUID         `json:"id"`
	ChannelID   uuid.UUID         `json:"channel_id"`
	StartDate   string            `json:"start_date"`
	EndDate     string            `json:"end_date"`
	Timezone    string            `json:"timezone"`
	Status      string            `json:"status"`
	NeedsReview bool              `json:"needs_review"`
	Goals       []StudioGoal      `json:"goals"`
	Review      *StudioGoalReview `json:"review,omitempty"`
}

type StudioGoalsResponse struct {
	CurrentCycle *StudioGoalCycle         `json:"current_cycle,omitempty"`
	Cycles       []StudioGoalCycle        `json:"cycles"`
	Metrics      []StudioGoalMetricOption `json:"metrics"`
}

type CreateStudioGoalCycleInput struct {
	ChannelID uuid.UUID `json:"channel_id"`
	StartDate string    `json:"start_date"`
	EndDate   string    `json:"end_date"`
	Timezone  string    `json:"timezone"`
}

type CreateStudioGoalInput struct {
	Name        string `json:"name"`
	Module      Module `json:"module"`
	Metric      string `json:"metric"`
	TargetValue int64  `json:"target_value"`
}

type UpdateStudioGoalInput struct {
	Name        *string `json:"name"`
	TargetValue *int64  `json:"target_value"`
}

type CreateStudioGoalActionInput struct {
	Title         string     `json:"title"`
	DueDate       string     `json:"due_date"`
	ContentID     *uuid.UUID `json:"content_id"`
	ContentModule Module     `json:"content_module"`
}

type UpdateStudioGoalActionInput struct {
	Title  *string `json:"title"`
	Status *string `json:"status"`
}

type CreateStudioGoalReviewInput struct {
	Result     string `json:"result"`
	Learning   string `json:"learning"`
	NextAction string `json:"next_action"`
}
