package feed

import (
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) MarkRead(user authctx.CurrentUser, ids []uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.MarkRead(user.ID, dedupeUUIDs(ids))
}

func (s *Service) MarkUnread(user authctx.CurrentUser, ids []uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeleteReads(user.ID, dedupeUUIDs(ids))
}

func (s *Service) MarkSubscriptionRead(user authctx.CurrentUser, subscriptionID uuid.UUID) error {
	ids, err := s.subscriptionFeedItemIDs(user, subscriptionID)
	if err != nil {
		return err
	}
	return s.repo.MarkRead(user.ID, ids)
}

func (s *Service) MarkSubscriptionUnread(user authctx.CurrentUser, subscriptionID uuid.UUID) error {
	ids, err := s.subscriptionFeedItemIDs(user, subscriptionID)
	if err != nil {
		return err
	}
	return s.repo.DeleteReads(user.ID, ids)
}

func (s *Service) subscriptionFeedItemIDs(user authctx.CurrentUser, subscriptionID uuid.UUID) ([]uuid.UUID, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	if subscriptionID == uuid.Nil {
		return nil, apperr.BadRequest("validation.invalid_request", "subscription id is required")
	}

	var subscription model.Subscription
	if err := s.db.Where("id = ? AND user_id = ?", subscriptionID, user.ID).First(&subscription).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.NotFound("feed.subscription_not_found", "Subscription not found")
		}
		return nil, err
	}

	var ids []uuid.UUID
	if err := s.db.Model(&model.FeedItem{}).
		Where("feed_source_id = ?", subscription.FeedSourceID).
		Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Service) MarkAllRead(user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	items, err := s.repo.ListSubscribedExternalFeedItems(user.ID)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return s.repo.MarkRead(user.ID, ids)
}

func (s *Service) MarkAllUnread(user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	items, err := s.repo.ListSubscribedExternalFeedItems(user.ID)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return s.repo.DeleteReads(user.ID, ids)
}

func (s *Service) ToggleStar(user authctx.CurrentUser, feedItemID uuid.UUID) (bool, error) {
	if user.ID == uuid.Nil {
		return false, apperr.Unauthorized("Login required")
	}
	if feedItemID == uuid.Nil {
		return false, apperr.BadRequest("validation.invalid_request", "feed_item_id is required")
	}
	if err := s.ensureFeedItemExists(feedItemID); err != nil {
		return false, err
	}
	_, err := s.repo.FindStar(user.ID, feedItemID)
	if err == nil {
		return false, s.repo.DeleteStar(user.ID, feedItemID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	star := model.FeedItemStar{UserID: user.ID, FeedItemID: feedItemID, StarredAt: time.Now()}
	if err := s.repo.CreateStar(&star); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) ToggleReadingList(user authctx.CurrentUser, targetType string, targetID uuid.UUID) (bool, error) {
	if user.ID == uuid.Nil {
		return false, apperr.Unauthorized("Login required")
	}
	if targetID == uuid.Nil {
		return false, apperr.BadRequest("validation.invalid_request", "target_id is required")
	}
	if err := s.ensureReadingListTarget(user, targetType, targetID); err != nil {
		return false, err
	}
	_, err := s.repo.FindReadingListItem(user.ID, targetType, targetID)
	if err == nil {
		return false, s.repo.DeleteReadingListItem(user.ID, targetType, targetID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	item := model.ReadingListItem{UserID: user.ID, TargetType: targetType, TargetID: targetID, CreatedAt: time.Now()}
	if err := s.repo.CreateReadingListItem(&item); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) ensureReadingListTarget(user authctx.CurrentUser, targetType string, targetID uuid.UUID) error {
	switch targetType {
	case "feed_item":
		return s.ensureFeedItemExists(targetID)
	case "post":
		var post model.Post
		if err := s.db.First(&post, "id = ?", targetID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("blog.post_not_found", "Post not found")
			}
			return err
		}
		if post.Status != "published" || (post.Visibility != "public" && post.UserID != user.ID) {
			return apperr.Forbidden("blog.post_forbidden", "You don't have permission to save this post")
		}
		return nil
	default:
		return apperr.BadRequest("validation.invalid_request", "target_type must be feed_item or post")
	}
}

func (s *Service) ListReadingList(user authctx.CurrentUser, query FeedQuery) ([]model.ReadingListItem, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page := normalizedPage(query.Page)
	limit := normalizedPageSize(query.PageSize)
	items, err := s.repo.ListReadingListItems(user.ID, limit, (page-1)*limit)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.repo.CountReadingListItems(user.ID)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Service) RemoveReadingListItem(user authctx.CurrentUser, targetType string, targetID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if targetID == uuid.Nil || (targetType != "feed_item" && targetType != "post") {
		return apperr.BadRequest("validation.invalid_request", "valid target_type and target_id are required")
	}
	return s.repo.DeleteReadingListItem(user.ID, targetType, targetID)
}

func (s *Service) RecordSourceReadEvent(sourceType string, sourceID string, eventType string) error {
	if sourceType == "" || sourceID == "" || eventType == "" {
		return apperr.BadRequest("validation.invalid_request", "source_type, source_id and event_type are required")
	}
	event := &model.SourceReadEvent{
		SourceType: sourceType,
		SourceID:   sourceID,
		EventType:  eventType,
	}
	return s.repo.CreateSourceReadEvent(event)
}

func (s *Service) readMap(userID uuid.UUID, items []model.FeedItem) (map[uuid.UUID]bool, error) {
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	reads, err := s.repo.ListReadItems(userID, ids)
	if err != nil {
		return nil, err
	}
	result := make(map[uuid.UUID]bool, len(reads))
	for _, read := range reads {
		result[read.FeedItemID] = true
	}
	return result, nil
}

func (s *Service) ensureFeedItemExists(feedItemID uuid.UUID) error {
	exists, err := s.repo.FeedItemExists(feedItemID)
	if err != nil {
		return err
	}
	if !exists {
		return apperr.NotFound("feed.feed_item_not_found", "Feed item not found")
	}
	return nil
}
