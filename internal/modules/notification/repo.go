package notification

import (
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) ListNotifications(recipientID uuid.UUID, query ListQuery) ([]model.Notification, int64, error) {
	var notifications []model.Notification
	var total int64

	query = normalizeListQuery(query)
	db := r.visibleNotifications(recipientID)
	if notifType := query.Type; notifType != "" {
		db = db.Where("type = ?", notifType)
	} else if category := query.Category; category != "" {
		db = filterNotificationCategory(db, category)
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
	err := r.visibleNotifications(recipientID).Where("read_at IS NULL").Count(&count).Error
	return count, err
}

type unreadTypeCount struct {
	Type  string
	Count int64
}

func (r *Repo) CountUnreadNotificationsByType(recipientID uuid.UUID) ([]unreadTypeCount, error) {
	var counts []unreadTypeCount
	err := r.visibleNotifications(recipientID).
		Select("type, COUNT(*) AS count").
		Where("read_at IS NULL").
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

func (r *Repo) MarkAllRead(recipientID uuid.UUID, query ListQuery, readAt time.Time) error {
	query = normalizeListQuery(query)
	db := r.db.Model(&model.Notification{}).Where("recipient_id = ? AND read_at IS NULL", recipientID)
	if query.Type != "" {
		db = db.Where("type = ?", query.Type)
	} else if query.Category != "" {
		db = filterNotificationCategory(db, query.Category)
	}
	return db.Update("read_at", readAt).Error
}

func (r *Repo) SavePreferences(userID uuid.UUID, items []model.NotificationPreference) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		for i := range items {
			items[i].UserID = userID
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "user_id"}, {Name: "event_type"}},
				DoUpdates: clause.AssignmentColumns([]string{"category", "enabled", "updated_at", "deleted_at"}),
			}).Create(&items[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repo) ListPreferences(userID uuid.UUID) ([]model.NotificationPreference, error) {
	var items []model.NotificationPreference
	err := r.db.Where("user_id = ?", userID).Order("category, event_type").Find(&items).Error
	return items, err
}

func (r *Repo) CreateMute(mute *model.NotificationMute) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "source_type"}, {Name: "source_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"reason", "updated_at", "deleted_at"}),
	}).Create(mute).Error
}

func (r *Repo) visibleNotifications(recipientID uuid.UUID) *gorm.DB {
	return r.db.Model(&model.Notification{}).
		Where("notifications.recipient_id = ?", recipientID).
		Where(`NOT EXISTS (
			SELECT 1 FROM notification_preferences
			WHERE notification_preferences.user_id = notifications.recipient_id
				AND notification_preferences.event_type = notifications.type
				AND notification_preferences.enabled = ?
				AND notification_preferences.deleted_at IS NULL
		)`, false).
		Where(`NOT EXISTS (
			SELECT 1 FROM notification_mutes
			WHERE notification_mutes.user_id = notifications.recipient_id
				AND notification_mutes.source_type = notifications.source_type
				AND notification_mutes.source_id = notifications.source_id
				AND notification_mutes.deleted_at IS NULL
		)`)
}

func filterNotificationCategory(db *gorm.DB, category string) *gorm.DB {
	if category == "system" {
		return db.Where("type NOT IN ?", knownNotificationTypes)
	}
	if types, ok := notificationTypesByCategory[category]; ok {
		return db.Where("type IN ?", types)
	}
	return db.Where("1 = 0")
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
