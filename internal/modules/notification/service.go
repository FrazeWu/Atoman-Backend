package notification

import (
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Service struct {
	db   *gorm.DB
	repo *Repo
}

var notificationCategories = [...]string{"like", "interaction", "mention", "reply", "collaboration", "system"}

func categoryForType(notificationType string) string {
	switch strings.TrimSpace(notificationType) {
	case "comment_like", "forum_like":
		return "like"
	case "comment_marked", "forum_follow", "forum_solved":
		return "interaction"
	case "comment_mention", "forum_mention":
		return "mention"
	case "comment_reply", "forum_reply", "forum_topic_comment":
		return "reply"
	case "collaboration.required":
		return "collaboration"
	default:
		return "system"
	}
}

func NewService(db *gorm.DB) *Service { return &Service{db: db, repo: NewRepo(db)} }

func (s *Service) ListNotifications(user authctx.CurrentUser, query ListQuery) ([]NotificationDTO, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	notifications, total, err := s.repo.ListNotifications(user.ID, query)
	if err != nil {
		return nil, 0, err
	}
	items := make([]NotificationDTO, 0, len(notifications))
	for _, notification := range notifications {
		items = append(items, toDTO(notification))
	}
	return items, total, nil
}

func (s *Service) GetUnreadCount(user authctx.CurrentUser) (int64, error) {
	if user.ID == uuid.Nil {
		return 0, apperr.Unauthorized("Login required")
	}
	return s.repo.CountUnreadNotifications(user.ID)
}

func (s *Service) GetUnreadCounts(user authctx.CurrentUser) (UnreadCountsDTO, error) {
	if user.ID == uuid.Nil {
		return UnreadCountsDTO{}, apperr.Unauthorized("Login required")
	}
	items := make(map[string]int64, len(notificationCategories)+1)
	for _, category := range notificationCategories {
		items[category] = 0
	}
	counts, err := s.repo.CountUnreadNotificationsByType(user.ID)
	if err != nil {
		return UnreadCountsDTO{}, err
	}
	var total int64
	for _, count := range counts {
		items[categoryForType(count.Type)] += count.Count
		total += count.Count
	}
	dmCount, err := s.repo.CountUnreadDM(user.ID)
	if err != nil {
		return UnreadCountsDTO{}, err
	}
	items["dm"] = dmCount
	total += dmCount
	return UnreadCountsDTO{Total: total, Items: items}, nil
}

func (s *Service) MarkRead(user authctx.CurrentUser, notificationID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if notificationID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "notification_id is required")
	}
	updated, err := s.repo.MarkRead(user.ID, notificationID, time.Now())
	if err != nil {
		return err
	}
	if !updated {
		return apperr.NotFound("notification.not_found", "Notification not found")
	}
	return nil
}

func (s *Service) MarkAllRead(user authctx.CurrentUser, notifType string) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.MarkAllRead(user.ID, strings.TrimSpace(notifType), time.Now())
}

func toDTO(notification model.Notification) NotificationDTO {
	dto := NotificationDTO{
		ID:         notification.ID.String(),
		Type:       notification.Type,
		Category:   categoryForType(notification.Type),
		SourceType: notification.SourceType,
		SourceID:   notification.SourceID.String(),
		Meta:       notification.Meta,
		ReadAt:     notification.ReadAt,
		CreatedAt:  notification.CreatedAt,
	}
	if notification.Actor != nil {
		dto.Actor = &ActorDTO{
			ID:          notification.Actor.UUID.String(),
			Username:    notification.Actor.Username,
			DisplayName: notification.Actor.DisplayName,
			AvatarURL:   notification.Actor.AvatarURL,
		}
	}
	return dto
}
