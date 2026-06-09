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
