package lifecycle

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Service struct{ db *gorm.DB }

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

type EventInput struct {
	Module        string    `json:"module"`
	ContentID     uuid.UUID `json:"content_id"`
	Event         string    `json:"event"`
	Source        string    `json:"source"`
	SessionID     string    `json:"session_id"`
	ClientEventID string    `json:"client_event_id"`
	PositionSec   int       `json:"position_sec"`
	DurationSec   int       `json:"duration_sec"`
	Progress      float64   `json:"progress"`
}

type ProgressInput struct {
	Module      string    `json:"module"`
	ContentID   uuid.UUID `json:"content_id"`
	PositionSec int       `json:"position_sec"`
	DurationSec int       `json:"duration_sec"`
	Progress    float64   `json:"progress"`
	Completed   bool      `json:"completed"`
	Source      string    `json:"source"`
}

type ContinueItem struct {
	ContentID   uuid.UUID `json:"content_id"`
	Module      string    `json:"module"`
	Title       string    `json:"title"`
	Path        string    `json:"path"`
	CoverURL    string    `json:"cover_url"`
	PositionSec int       `json:"position_sec"`
	DurationSec int       `json:"duration_sec"`
	Progress    float64   `json:"progress"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type NotificationPreferenceInput struct {
	SourceType string    `json:"source_type"`
	SourceID   uuid.UUID `json:"source_id"`
	Mode       string    `json:"mode"`
}

type ScheduleInput struct {
	Module    string    `json:"module"`
	ContentID uuid.UUID `json:"content_id"`
	PublishAt time.Time `json:"publish_at"`
}

type ScheduleResult struct {
	Module    string    `json:"module"`
	ContentID uuid.UUID `json:"content_id"`
	Status    string    `json:"status"`
	PublishAt time.Time `json:"publish_at"`
}

type contentSummary struct {
	Module, Title, Path, CoverURL, Status, Visibility string
	ContentID, ChannelID, OwnerID                     uuid.UUID
	DurationSec                                       int
}

var allowedEvents = map[string]bool{
	"impression": true, "open": true, "engaged": true, "complete": true,
	"like": true, "comment": true, "bookmark": true, "share": true, "follow": true,
}

func (s *Service) RecordEvent(user authctx.CurrentUser, input EventInput) error {
	input.Module = normalizeModule(input.Module)
	input.Event = strings.TrimSpace(strings.ToLower(input.Event))
	if !allowedEvents[input.Event] {
		return apperr.BadRequest("lifecycle.invalid_event", "event is not supported")
	}
	content, err := s.resolveContent(input.Module, input.ContentID)
	if err != nil {
		return err
	}
	if content.Status != "published" || content.Visibility == "private" {
		return apperr.NotFound("lifecycle.content_not_found", "Content not found")
	}
	clientEventID := strings.TrimSpace(input.ClientEventID)
	if clientEventID == "" {
		clientEventID = uuid.NewString()
	}
	var userID *uuid.UUID
	if user.ID != uuid.Nil {
		id := user.ID
		userID = &id
	}
	event := model.ContentLifecycleEvent{
		UserID: userID, ChannelID: content.ChannelID, ContentType: input.Module, ContentID: input.ContentID,
		Event: input.Event, Source: normalizeSource(input.Source), SessionID: strings.TrimSpace(input.SessionID),
		ClientEventID: clientEventID, PositionSec: max(0, input.PositionSec), DurationSec: max(0, input.DurationSec),
		Progress: clampProgress(input.Progress),
	}
	return s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "client_event_id"}}, DoNothing: true}).Create(&event).Error
}

func (s *Service) SaveProgress(user authctx.CurrentUser, input ProgressInput) (model.ContentProgress, error) {
	if user.ID == uuid.Nil {
		return model.ContentProgress{}, apperr.Unauthorized("Login required")
	}
	input.Module = normalizeModule(input.Module)
	content, err := s.resolveContent(input.Module, input.ContentID)
	if err != nil {
		return model.ContentProgress{}, err
	}
	if content.Status != "published" || content.Visibility == "private" {
		return model.ContentProgress{}, apperr.NotFound("lifecycle.content_not_found", "Content not found")
	}
	progress := clampProgress(input.Progress)
	completed := input.Completed || progress >= 0.95
	record := model.ContentProgress{
		UserID: user.ID, ChannelID: content.ChannelID, ContentType: input.Module, ContentID: input.ContentID,
		PositionSec: max(0, input.PositionSec), DurationSec: max(0, input.DurationSec), Progress: progress,
		Completed: completed, Source: normalizeSource(input.Source),
	}
	err = s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "content_type"}, {Name: "content_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"channel_id", "position_sec", "duration_sec", "progress", "completed", "source", "updated_at"}),
	}).Create(&record).Error
	if err != nil {
		return model.ContentProgress{}, err
	}
	record = model.ContentProgress{}
	if err := s.db.Where("user_id = ? AND content_type = ? AND content_id = ?", user.ID, input.Module, input.ContentID).First(&record).Error; err != nil {
		return model.ContentProgress{}, err
	}
	return record, nil
}

func (s *Service) ListContinue(user authctx.CurrentUser, module string, limit int) ([]ContinueItem, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	module = normalizeModule(module)
	if limit < 1 || limit > 50 {
		limit = 12
	}
	var rows []model.ContentProgress
	if err := s.db.Where("user_id = ? AND content_type = ? AND completed = ? AND progress > 0", user.ID, module, false).
		Order("updated_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]ContinueItem, 0, len(rows))
	for _, row := range rows {
		content, err := s.resolveContent(module, row.ContentID)
		if err != nil || content.Status != "published" || content.Visibility == "private" {
			continue
		}
		items = append(items, ContinueItem{
			ContentID: row.ContentID, Module: module, Title: content.Title, Path: content.Path, CoverURL: content.CoverURL,
			PositionSec: row.PositionSec, DurationSec: row.DurationSec, Progress: row.Progress, UpdatedAt: row.UpdatedAt,
		})
	}
	return items, nil
}

func (s *Service) GetProgress(user authctx.CurrentUser, module string, contentID uuid.UUID) (*model.ContentProgress, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	module = normalizeModule(module)
	var progress model.ContentProgress
	err := s.db.Where("user_id = ? AND content_type = ? AND content_id = ?", user.ID, module, contentID).First(&progress).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &progress, nil
}

func (s *Service) SaveNotificationPreference(user authctx.CurrentUser, input NotificationPreferenceInput) (model.ContentNotificationPreference, error) {
	if user.ID == uuid.Nil {
		return model.ContentNotificationPreference{}, apperr.Unauthorized("Login required")
	}
	if input.SourceType != "internal_user" && input.SourceType != "internal_channel" {
		return model.ContentNotificationPreference{}, apperr.BadRequest("lifecycle.invalid_source", "source_type is invalid")
	}
	if input.SourceID == uuid.Nil {
		return model.ContentNotificationPreference{}, apperr.BadRequest("validation.invalid_request", "source_id is required")
	}
	if input.Mode != "feed_only" && input.Mode != "all" && input.Mode != "daily" {
		return model.ContentNotificationPreference{}, apperr.BadRequest("lifecycle.invalid_notification_mode", "mode is invalid")
	}
	record := model.ContentNotificationPreference{UserID: user.ID, SourceType: input.SourceType, SourceID: input.SourceID, Mode: input.Mode}
	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "source_type"}, {Name: "source_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"mode", "updated_at"}),
	}).Create(&record).Error
	if err != nil {
		return model.ContentNotificationPreference{}, err
	}
	if err := s.db.Where("user_id = ? AND source_type = ? AND source_id = ?", user.ID, input.SourceType, input.SourceID).First(&record).Error; err != nil {
		return model.ContentNotificationPreference{}, err
	}
	return record, nil
}

func (s *Service) ListNotificationPreferences(user authctx.CurrentUser) ([]model.ContentNotificationPreference, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	var preferences []model.ContentNotificationPreference
	if err := s.db.Where("user_id = ?", user.ID).Order("updated_at DESC").Find(&preferences).Error; err != nil {
		return nil, err
	}
	return preferences, nil
}

func (s *Service) EnqueuePublication(module string, contentID uuid.UUID) error {
	module = normalizeModule(module)
	content, err := s.resolveContent(module, contentID)
	if err != nil {
		return err
	}
	if content.Status != "published" {
		return apperr.BadRequest("lifecycle.content_not_published", "Content is not published")
	}
	event := model.ContentPublicationEvent{ChannelID: content.ChannelID, OwnerID: content.OwnerID, ContentType: module, ContentID: contentID, Status: "pending"}
	return s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "content_type"}, {Name: "content_id"}}, DoNothing: true}).Create(&event).Error
}

func (s *Service) ScheduleContent(user authctx.CurrentUser, input ScheduleInput) (ScheduleResult, error) {
	if user.ID == uuid.Nil {
		return ScheduleResult{}, apperr.Unauthorized("Login required")
	}
	input.Module = normalizeModule(input.Module)
	content, err := s.resolveContent(input.Module, input.ContentID)
	if err != nil {
		return ScheduleResult{}, err
	}
	if content.OwnerID != user.ID {
		return ScheduleResult{}, apperr.Forbidden("lifecycle.content_forbidden", "You do not have permission to schedule this content")
	}
	publishAt := input.PublishAt.UTC()
	if !publishAt.After(time.Now().UTC()) {
		return ScheduleResult{}, apperr.BadRequest("lifecycle.invalid_publish_at", "publish_at must be in the future")
	}
	if err := s.validatePublishable(input.Module, input.ContentID, false); err != nil {
		return ScheduleResult{}, err
	}
	switch input.Module {
	case "blog":
		if err := s.db.Model(&model.Post{}).Where("id = ? AND user_id = ?", input.ContentID, user.ID).Updates(map[string]any{"status": "scheduled", "scheduled_at": publishAt}).Error; err != nil {
			return ScheduleResult{}, err
		}
	case "podcast":
		var episode model.PodcastEpisode
		if err := s.db.First(&episode, "id = ?", input.ContentID).Error; err != nil {
			return ScheduleResult{}, contentError(err)
		}
		if err := s.db.Model(&model.Post{}).Where("id = ? AND user_id = ?", episode.PostID, user.ID).Updates(map[string]any{"status": "scheduled", "scheduled_at": publishAt}).Error; err != nil {
			return ScheduleResult{}, err
		}
	case "video":
		if err := s.db.Model(&model.Video{}).Where("id = ? AND user_id = ?", input.ContentID, user.ID).Updates(map[string]any{"status": "scheduled", "scheduled_at": publishAt}).Error; err != nil {
			return ScheduleResult{}, err
		}
	}
	return ScheduleResult{Module: input.Module, ContentID: input.ContentID, Status: "scheduled", PublishAt: publishAt}, nil
}

func (s *Service) CancelSchedule(user authctx.CurrentUser, module string, contentID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	module = normalizeModule(module)
	content, err := s.resolveContent(module, contentID)
	if err != nil {
		return err
	}
	if content.OwnerID != user.ID {
		return apperr.Forbidden("lifecycle.content_forbidden", "You do not have permission to cancel this schedule")
	}
	switch module {
	case "blog":
		return s.db.Model(&model.Post{}).Where("id = ? AND status = ?", contentID, "scheduled").Updates(map[string]any{"status": "draft", "scheduled_at": nil}).Error
	case "podcast":
		var episode model.PodcastEpisode
		if err := s.db.First(&episode, "id = ?", contentID).Error; err != nil {
			return contentError(err)
		}
		return s.db.Model(&model.Post{}).Where("id = ? AND status = ?", episode.PostID, "scheduled").Updates(map[string]any{"status": "draft", "scheduled_at": nil}).Error
	case "video":
		return s.db.Model(&model.Video{}).Where("id = ? AND status = ?", contentID, "scheduled").Updates(map[string]any{"status": "draft", "scheduled_at": nil}).Error
	default:
		return apperr.BadRequest("lifecycle.invalid_module", "module must be blog, podcast, or video")
	}
}

func (s *Service) PublishDue(now time.Time, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	now = now.UTC()
	var posts []model.Post
	if err := s.db.Where("status = ? AND scheduled_at IS NOT NULL AND scheduled_at <= ?", "scheduled", now).Order("scheduled_at ASC").Limit(limit).Find(&posts).Error; err != nil {
		return err
	}
	for _, post := range posts {
		module := "blog"
		contentID := post.ID
		var episode model.PodcastEpisode
		err := s.db.First(&episode, "post_id = ?", post.ID).Error
		if err == nil {
			module = "podcast"
			contentID = episode.ID
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := s.validatePublishable(module, contentID, true); err != nil {
			continue
		}
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.Post{}).Where("id = ? AND status = ?", post.ID, "scheduled").Updates(map[string]any{"status": "published", "published_at": now, "scheduled_at": nil}).Error; err != nil {
				return err
			}
			return enqueuePublication(tx, module, contentID, *post.ChannelID, post.UserID)
		}); err != nil {
			return err
		}
	}
	remaining := limit - len(posts)
	if remaining <= 0 {
		return nil
	}
	var videos []model.Video
	if err := s.db.Where("status = ? AND scheduled_at IS NOT NULL AND scheduled_at <= ?", "scheduled", now).Order("scheduled_at ASC").Limit(remaining).Find(&videos).Error; err != nil {
		return err
	}
	for _, video := range videos {
		if err := s.validatePublishable("video", video.ID, true); err != nil {
			continue
		}
		if video.StorageType == "local" && video.ProcessingStatus != "ready" {
			continue
		}
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.Video{}).Where("id = ? AND status = ?", video.ID, "scheduled").Updates(map[string]any{"status": "published", "published_at": now, "scheduled_at": nil}).Error; err != nil {
				return err
			}
			return enqueuePublication(tx, "video", video.ID, *video.ChannelID, video.UserID)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) validatePublishable(module string, contentID uuid.UUID, requireReady bool) error {
	switch module {
	case "blog":
		var post model.Post
		if err := s.db.First(&post, "id = ?", contentID).Error; err != nil {
			return contentError(err)
		}
		if strings.TrimSpace(post.Title) == "" || strings.TrimSpace(post.Content) == "" || !s.postHasCollection(post) {
			return apperr.BadRequest("lifecycle.publish_check_failed", "Blog title, content, and collection are required")
		}
	case "podcast":
		var episode model.PodcastEpisode
		if err := s.db.Preload("Post").First(&episode, "id = ?", contentID).Error; err != nil {
			return contentError(err)
		}
		if episode.Post == nil || strings.TrimSpace(episode.Post.Title) == "" || strings.TrimSpace(episode.AudioURL) == "" || !s.postHasCollection(*episode.Post) {
			return apperr.BadRequest("lifecycle.publish_check_failed", "Podcast title, audio, and collection are required")
		}
	case "video":
		var video model.Video
		if err := s.db.First(&video, "id = ?", contentID).Error; err != nil {
			return contentError(err)
		}
		var collections int64
		if err := s.db.Model(&model.VideoCollection{}).Where("video_id = ?", video.ID).Count(&collections).Error; err != nil {
			return err
		}
		if strings.TrimSpace(video.Title) == "" || strings.TrimSpace(video.VideoURL) == "" || collections == 0 {
			return apperr.BadRequest("lifecycle.publish_check_failed", "Video title, source, and collection are required")
		}
		if requireReady && video.StorageType == "local" && video.ProcessingStatus != "ready" {
			return apperr.Conflict("lifecycle.processing_pending", "Video processing is not ready")
		}
	default:
		return apperr.BadRequest("lifecycle.invalid_module", "module must be blog, podcast, or video")
	}
	return nil
}

func (s *Service) ValidatePublishable(module string, contentID uuid.UUID) error {
	return s.validatePublishable(normalizeModule(module), contentID, true)
}

func (s *Service) postHasCollection(post model.Post) bool {
	if post.CollectionID != nil {
		return true
	}
	var count int64
	if err := s.db.Model(&model.PostCollection{}).Where("post_id = ?", post.ID).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

func enqueuePublication(tx *gorm.DB, module string, contentID, channelID, ownerID uuid.UUID) error {
	event := model.ContentPublicationEvent{ChannelID: channelID, OwnerID: ownerID, ContentType: module, ContentID: contentID, Status: "pending"}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "content_type"}, {Name: "content_id"}}, DoNothing: true}).Create(&event).Error
}

func (s *Service) DispatchPendingPublications(limit int) error {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var events []model.ContentPublicationEvent
	if err := s.db.Where("status = ?", "pending").Order("created_at ASC").Limit(limit).Find(&events).Error; err != nil {
		return err
	}
	for _, event := range events {
		if err := s.dispatchPublication(event); err != nil {
			_ = s.db.Model(&event).Updates(map[string]any{"attempts": event.Attempts + 1, "last_error": err.Error()}).Error
			continue
		}
		now := time.Now().UTC()
		if err := s.db.Model(&event).Updates(map[string]any{"status": "delivered", "dispatched_at": &now, "last_error": ""}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) dispatchPublication(event model.ContentPublicationEvent) error {
	content, err := s.resolveContent(event.ContentType, event.ContentID)
	if err != nil {
		return err
	}
	if content.Status != "published" || content.Visibility == "private" {
		return nil
	}
	recipients, err := s.publicationRecipients(event.OwnerID, event.ChannelID)
	if err != nil {
		return err
	}
	for _, recipientID := range recipients {
		if recipientID == event.OwnerID {
			continue
		}
		var enabled int64
		if err := s.db.Model(&model.ContentNotificationPreference{}).Where(
			"user_id = ? AND mode = ? AND ((source_type = ? AND source_id = ?) OR (source_type = ? AND source_id = ?))",
			recipientID, "all", "internal_channel", event.ChannelID, "internal_user", event.OwnerID,
		).Count(&enabled).Error; err != nil {
			return err
		}
		if enabled == 0 {
			continue
		}
		var exists int64
		if err := s.db.Model(&model.Notification{}).Where("recipient_id = ? AND source_type = ? AND source_id = ?", recipientID, "content_publication", event.ContentID).Count(&exists).Error; err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		actorID := event.OwnerID
		notification := model.Notification{
			RecipientID: recipientID, ActorID: &actorID, Type: "content_published", SourceType: "content_publication", SourceID: event.ContentID,
			Meta: model.NotificationMeta{"module": event.ContentType, "title": content.Title, "path": content.Path, "channel_id": event.ChannelID.String()},
		}
		if err := s.db.Create(&notification).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) publicationRecipients(ownerID, channelID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	var subscriptionIDs []uuid.UUID
	if err := s.db.Model(&model.Subscription{}).
		Select("subscriptions.user_id").Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id AND feed_sources.deleted_at IS NULL").
		Where("subscriptions.deleted_at IS NULL AND ((feed_sources.source_type = ? AND feed_sources.source_id = ?) OR (feed_sources.source_type = ? AND feed_sources.source_id = ?))", "internal_channel", channelID, "internal_user", ownerID).
		Pluck("subscriptions.user_id", &subscriptionIDs).Error; err != nil {
		return nil, err
	}
	ids = append(ids, subscriptionIDs...)
	if s.db.Migrator().HasTable(&model.Follow{}) {
		var followerIDs []uuid.UUID
		if err := s.db.Model(&model.Follow{}).Where("following_id = ?", ownerID).Pluck("follower_id", &followerIDs).Error; err != nil {
			return nil, err
		}
		ids = append(ids, followerIDs...)
	}
	seen := make(map[uuid.UUID]struct{}, len(ids))
	result := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}

func (s *Service) resolveContent(module string, id uuid.UUID) (contentSummary, error) {
	if id == uuid.Nil {
		return contentSummary{}, apperr.BadRequest("validation.invalid_request", "content_id is required")
	}
	switch normalizeModule(module) {
	case "blog":
		var post model.Post
		if err := s.db.First(&post, "id = ?", id).Error; err != nil {
			return contentSummary{}, contentError(err)
		}
		if post.ChannelID == nil {
			return contentSummary{}, apperr.NotFound("lifecycle.content_not_found", "Content not found")
		}
		var episodes int64
		if err := s.db.Model(&model.PodcastEpisode{}).Where("post_id = ?", post.ID).Count(&episodes).Error; err != nil {
			return contentSummary{}, err
		}
		if episodes > 0 {
			return contentSummary{}, apperr.NotFound("lifecycle.content_not_found", "Content not found")
		}
		return contentSummary{Module: "blog", ContentID: id, ChannelID: *post.ChannelID, OwnerID: post.UserID, Title: post.Title, Path: "/posts/post/" + id.String(), CoverURL: post.CoverURL, Status: post.Status, Visibility: post.Visibility}, nil
	case "podcast":
		var episode model.PodcastEpisode
		if err := s.db.Preload("Post").First(&episode, "id = ?", id).Error; err != nil {
			return contentSummary{}, contentError(err)
		}
		if episode.Post == nil {
			return contentSummary{}, apperr.NotFound("lifecycle.content_not_found", "Content not found")
		}
		return contentSummary{Module: "podcast", ContentID: id, ChannelID: episode.ChannelID, OwnerID: episode.Post.UserID, Title: episode.Post.Title, Path: "/podcasts/episode/" + id.String(), CoverURL: episode.EpisodeCoverURL, Status: episode.Post.Status, Visibility: episode.Post.Visibility, DurationSec: episode.DurationSec}, nil
	case "video":
		var video model.Video
		if err := s.db.First(&video, "id = ?", id).Error; err != nil {
			return contentSummary{}, contentError(err)
		}
		if video.ChannelID == nil {
			return contentSummary{}, apperr.NotFound("lifecycle.content_not_found", "Content not found")
		}
		return contentSummary{Module: "video", ContentID: id, ChannelID: *video.ChannelID, OwnerID: video.UserID, Title: video.Title, Path: "/videos/watch/" + id.String(), CoverURL: video.ThumbnailURL, Status: video.Status, Visibility: video.Visibility, DurationSec: video.DurationSec}, nil
	default:
		return contentSummary{}, apperr.BadRequest("lifecycle.invalid_module", "module must be blog, podcast, or video")
	}
}

func normalizeModule(value string) string { return strings.TrimSpace(strings.ToLower(value)) }
func normalizeSource(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "direct"
	}
	return value
}
func clampProgress(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
func contentError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("lifecycle.content_not_found", "Content not found")
	}
	return fmt.Errorf("resolve content: %w", err)
}
