package comment

import (
	"atoman/internal/model"

	"github.com/google/uuid"
)

type NotificationPublisher func(recipientID uuid.UUID, notification *model.Notification)

func (s *Service) SetNotificationPublisher(publisher NotificationPublisher) {
	s.notify = publisher
}

func (s *Service) publishCreatedCommentNotifications(commentID uuid.UUID) {
	if s.notify == nil {
		return
	}
	var notifications []model.Notification
	if err := s.db.Preload("Actor").Where("source_type = ? AND source_id = ?", "comment_event", commentID).Find(&notifications).Error; err != nil {
		return
	}
	for i := range notifications {
		notification := notifications[i]
		s.notify(notification.RecipientID, &notification)
	}
}
