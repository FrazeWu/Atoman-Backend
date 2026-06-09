package service

import (
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
)

var notificationPriority = map[string]int{
	"forum_mention": 3,
	"forum_reply":   2,
	"forum_solved":  1,
	"forum_like":    0,
}

type NotificationService struct {
	db *gorm.DB
}

func NewNotificationService(db *gorm.DB) *NotificationService {
	return &NotificationService{db: db}
}

func (s *NotificationService) CreateNotification(recipientID uuid.UUID, actorID *uuid.UUID, notifType, sourceType string, sourceID uuid.UUID, meta model.NotificationMeta) (*model.Notification, error) {
	if actorID != nil && *actorID == recipientID {
		return nil, nil
	}

	if notifType == "forum_like" {
		return s.upsertLikeNotification(recipientID, actorID, sourceType, sourceID, meta)
	}

	var existing model.Notification
	err := s.db.Where("recipient_id = ? AND source_type = ? AND source_id = ?", recipientID, sourceType, sourceID).First(&existing).Error
	if err == nil {
		existingPriority := notificationPriority[existing.Type]
		newPriority := notificationPriority[notifType]
		if newPriority < existingPriority {
			return nil, nil
		}
		existing.Type = notifType
		existing.ActorID = actorID
		existing.Meta = meta
		existing.ReadAt = nil
		if saveErr := s.db.Save(&existing).Error; saveErr != nil {
			return nil, saveErr
		}
		return &existing, nil
	}
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}

	notification := &model.Notification{
		RecipientID: recipientID,
		ActorID:     actorID,
		Type:        notifType,
		SourceType:  sourceType,
		SourceID:    sourceID,
		Meta:        meta,
	}
	if err := s.db.Create(notification).Error; err != nil {
		return nil, err
	}
	return notification, nil
}

func (s *NotificationService) upsertLikeNotification(recipientID uuid.UUID, actorID *uuid.UUID, sourceType string, sourceID uuid.UUID, meta model.NotificationMeta) (*model.Notification, error) {
	var existing model.Notification
	err := s.db.Where("recipient_id = ? AND source_type = ? AND source_id = ? AND type = ?", recipientID, sourceType, sourceID, "forum_like").First(&existing).Error

	actorUsername, _ := meta["actor_username"].(string)
	if err == nil {
		existing.Meta = mergeLikeMeta(existing.Meta, meta, actorUsername)
		existing.ActorID = actorID
		existing.ReadAt = nil
		if saveErr := s.db.Save(&existing).Error; saveErr != nil {
			return nil, saveErr
		}
		return &existing, nil
	}
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}

	meta = mergeLikeMeta(model.NotificationMeta{}, meta, actorUsername)
	notification := &model.Notification{
		RecipientID: recipientID,
		ActorID:     actorID,
		Type:        "forum_like",
		SourceType:  sourceType,
		SourceID:    sourceID,
		Meta:        meta,
	}
	if err := s.db.Create(notification).Error; err != nil {
		return nil, err
	}
	return notification, nil
}

func (s *NotificationService) MarkRead(notifID, recipientID uuid.UUID) error {
	now := time.Now()
	return s.db.Model(&model.Notification{}).
		Where("id = ? AND recipient_id = ?", notifID, recipientID).
		Update("read_at", now).Error
}

func (s *NotificationService) MarkAllRead(recipientID uuid.UUID, notifType string) error {
	now := time.Now()
	query := s.db.Model(&model.Notification{}).Where("recipient_id = ? AND read_at IS NULL", recipientID)
	if notifType != "" {
		query = query.Where("type = ?", notifType)
	}
	return query.Update("read_at", now).Error
}

func (s *NotificationService) UnreadCount(recipientID uuid.UUID) (int64, error) {
	var count int64
	err := s.db.Model(&model.Notification{}).
		Where("recipient_id = ? AND read_at IS NULL", recipientID).
		Count(&count).Error
	return count, err
}

func (s *NotificationService) List(recipientID uuid.UUID, notifType string, page, pageSize int) ([]model.Notification, int64, error) {
	var notifications []model.Notification
	var total int64

	query := s.db.Model(&model.Notification{}).Where("recipient_id = ?", recipientID)
	if notifType != "" {
		query = query.Where("type = ?", notifType)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	if err := query.Preload("Actor").Order("created_at DESC").Limit(pageSize).Offset(offset).Find(&notifications).Error; err != nil {
		return nil, 0, err
	}
	return notifications, total, nil
}

func (s *NotificationService) NotifyForumReply(reply *model.ForumReply, topic *model.ForumTopic, wsPush func(uuid.UUID, *model.Notification)) error {
	actorID := reply.UserID
	meta := model.NotificationMeta{
		"topic_id":      topic.ID.String(),
		"topic_title":   topic.Title,
		"reply_excerpt": truncateNotificationText(reply.Content, 100),
	}

	if topic.UserID != actorID {
		notification, err := s.CreateNotification(topic.UserID, &actorID, "forum_reply", "forum_reply", reply.ID, cloneNotificationMeta(meta))
		if err != nil {
			return err
		}
		if wsPush != nil {
			wsPush(topic.UserID, notification)
		}
	}

	if reply.ParentReplyID != nil {
		var parentReply model.ForumReply
		if err := s.db.Select("id", "user_id").First(&parentReply, "id = ?", *reply.ParentReplyID).Error; err == nil {
			if parentReply.UserID != actorID && parentReply.UserID != topic.UserID {
				notification, err := s.CreateNotification(parentReply.UserID, &actorID, "forum_reply", "forum_reply", reply.ID, cloneNotificationMeta(meta))
				if err != nil {
					return err
				}
				if notification != nil && wsPush != nil {
					wsPush(parentReply.UserID, notification)
				}
			}
		}
	}

	mentionedUsers, err := ParseMentions(s.db, reply.Content)
	if err != nil {
		return err
	}
	for _, user := range mentionedUsers {
		if user.UUID == actorID {
			continue
		}
		notification, err := s.CreateNotification(user.UUID, &actorID, "forum_mention", "forum_reply", reply.ID, cloneNotificationMeta(meta))
		if err != nil {
			return err
		}
		if wsPush != nil {
			wsPush(user.UUID, notification)
		}
	}

	return nil
}

func (s *NotificationService) NotifyForumSolved(reply *model.ForumReply, topic *model.ForumTopic, actorID uuid.UUID, wsPush func(uuid.UUID, *model.Notification)) error {
	meta := model.NotificationMeta{
		"topic_id":    topic.ID.String(),
		"topic_title": topic.Title,
	}
	notification, err := s.CreateNotification(reply.UserID, &actorID, "forum_solved", "forum_reply", reply.ID, meta)
	if err != nil {
		return err
	}
	if wsPush != nil {
		wsPush(reply.UserID, notification)
	}
	return nil
}

func (s *NotificationService) NotifyForumLike(ownerID, actorID uuid.UUID, actorUsername, sourceType string, sourceID, topicID uuid.UUID, topicTitle string, wsPush func(uuid.UUID, *model.Notification)) error {
	meta := model.NotificationMeta{
		"topic_id":       topicID.String(),
		"topic_title":    topicTitle,
		"actor_username": actorUsername,
	}
	notification, err := s.CreateNotification(ownerID, &actorID, "forum_like", sourceType, sourceID, meta)
	if err != nil {
		return err
	}
	if wsPush != nil {
		wsPush(ownerID, notification)
	}
	return nil
}

func cloneNotificationMeta(meta model.NotificationMeta) model.NotificationMeta {
	cloned := make(model.NotificationMeta, len(meta))
	for k, v := range meta {
		cloned[k] = v
	}
	return cloned
}

func truncateNotificationText(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "…"
}

func mergeLikeMeta(existing model.NotificationMeta, incoming model.NotificationMeta, actorUsername string) model.NotificationMeta {
	merged := cloneNotificationMeta(incoming)
	for key, value := range existing {
		if _, ok := merged[key]; !ok {
			merged[key] = value
		}
	}

	count := 0
	switch v := existing["actor_count"].(type) {
	case float64:
		count = int(v)
	case int:
		count = v
	case int64:
		count = int(v)
	}
	count++
	merged["actor_count"] = count

	recentActors := extractRecentActors(existing["recent_actors"])
	if actorUsername != "" {
		recentActors = append(recentActors, actorUsername)
	}
	merged["recent_actors"] = keepLastUnique(recentActors, 3)
	return merged
}

func extractRecentActors(value interface{}) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func keepLastUnique(items []string, limit int) []string {
	if len(items) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, limit)
	for i := len(items) - 1; i >= 0 && len(result) < limit; i-- {
		item := items[i]
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool { return i > j })
	return result
}
