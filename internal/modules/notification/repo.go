package notification

import (
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) ListNotifications(recipientID uuid.UUID, query ListQuery) ([]model.Notification, int64, error) {
	var notifications []model.Notification
	var total int64

	db := r.db.Model(&model.Notification{}).Where("recipient_id = ?", recipientID)
	if notifType := strings.TrimSpace(query.Type); notifType != "" {
		db = db.Where("type = ?", notifType)
	}
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page := normalizedPage(query.Page)
	pageSize := normalizedPageSize(query.PageSize)
	if err := db.Preload("Actor").Order("created_at DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&notifications).Error; err != nil {
		return nil, 0, err
	}
	return notifications, total, nil
}

func (r *Repo) CountUnreadNotifications(recipientID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.Model(&model.Notification{}).Where("recipient_id = ? AND read_at IS NULL", recipientID).Count(&count).Error
	return count, err
}

type unreadTypeCount struct {
	Type  string
	Count int64
}

func (r *Repo) CountUnreadNotificationsByType(recipientID uuid.UUID) ([]unreadTypeCount, error) {
	var counts []unreadTypeCount
	err := r.db.Model(&model.Notification{}).
		Select("type, COUNT(*) AS count").
		Where("recipient_id = ? AND read_at IS NULL", recipientID).
		Group("type").
		Scan(&counts).Error
	return counts, err
}

func (r *Repo) CountUnreadDM(recipientID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.Model(&model.DMMessage{}).
		Joins("JOIN dm_conversations ON dm_conversations.id = dm_messages.conversation_id").
		Joins("LEFT JOIN channels ON channels.id = dm_conversations.participant_b AND dm_conversations.participant_b_type = ?", model.DMPartyChannel).
		Where("dm_messages.read_at IS NULL").
		Where(`
			(dm_conversations.participant_b_type = ? AND (dm_conversations.participant_a = ? OR dm_conversations.participant_b = ?) AND dm_messages.sender_id != ?)
			OR (dm_conversations.participant_b_type = ? AND dm_conversations.participant_a = ? AND dm_messages.sender_type = ?)
			OR (dm_conversations.participant_b_type = ? AND channels.user_id = ? AND dm_messages.sender_type = ?)
		`, model.DMPartyUser, recipientID, recipientID, recipientID, model.DMPartyChannel, recipientID, model.DMPartyChannel, model.DMPartyChannel, recipientID, model.DMPartyUser).
		Count(&count).Error
	return count, err
}

func (r *Repo) MarkRead(recipientID uuid.UUID, notificationID uuid.UUID, readAt time.Time) (bool, error) {
	result := r.db.Model(&model.Notification{}).
		Where("id = ? AND recipient_id = ?", notificationID, recipientID).
		Update("read_at", readAt)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (r *Repo) MarkAllRead(recipientID uuid.UUID, notifType string, readAt time.Time) error {
	db := r.db.Model(&model.Notification{}).Where("recipient_id = ? AND read_at IS NULL", recipientID)
	if notifType = strings.TrimSpace(notifType); notifType != "" {
		db = db.Where("type = ?", notifType)
	}
	return db.Update("read_at", readAt).Error
}

func normalizedPage(page int) int {
	if page < 1 {
		return 1
	}
	return page
}

func normalizedPageSize(pageSize int) int {
	if pageSize < 1 {
		return 20
	}
	if pageSize > 100 {
		return 100
	}
	return pageSize
}
