package notification

import (
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repo struct{ db *gorm.DB }

type announcementRecord struct {
	Notification model.Notification
	PublishedAt  time.Time
	Delivered    int64
}

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

func (r *Repo) ListAnnouncements(query ListAnnouncementsQuery) ([]announcementRecord, int64, error) {
	base := r.db.Model(&model.Notification{}).
		Where("source_type = ?", announcementNotificationType)
	if search := strings.TrimSpace(query.Search); search != "" {
		base = base.Where("CAST(meta AS TEXT) ILIKE ?", "%"+search+"%")
	}
	if status := strings.TrimSpace(query.Status); status != "" && status != "all" && status != "delivered" {
		base = base.Where("1 = 0")
	}

	var total int64
	if err := base.Distinct("source_id").Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page := normalizedPage(query.Page)
	pageSize := normalizedPageSize(query.PageSize)
	var groups []struct {
		SourceID    uuid.UUID
		PublishedAt time.Time
	}
	if err := base.Select("source_id, MIN(created_at) AS published_at").
		Group("source_id").
		Order("published_at DESC").
		Order("source_id DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&groups).Error; err != nil {
		return nil, 0, err
	}
	if len(groups) == 0 {
		return []announcementRecord{}, total, nil
	}

	sourceIDs := make([]uuid.UUID, 0, len(groups))
	publishedAtBySource := make(map[uuid.UUID]time.Time, len(groups))
	for _, group := range groups {
		sourceIDs = append(sourceIDs, group.SourceID)
		publishedAtBySource[group.SourceID] = group.PublishedAt
	}

	var representatives []model.Notification
	if err := base.Where("source_id IN ?", sourceIDs).
		Select("DISTINCT ON (source_id) *").
		Preload("Actor").
		Order("source_id, created_at ASC, id ASC").
		Find(&representatives).Error; err != nil {
		return nil, 0, err
	}

	var counts []struct {
		SourceID uuid.UUID
		Count    int64
	}
	if err := base.Where("source_id IN ?", sourceIDs).
		Select("source_id, COUNT(*) AS count").
		Group("source_id").
		Scan(&counts).Error; err != nil {
		return nil, 0, err
	}
	countBySource := make(map[uuid.UUID]int64, len(counts))
	for _, count := range counts {
		countBySource[count.SourceID] = count.Count
	}
	representativeBySource := make(map[uuid.UUID]model.Notification, len(representatives))
	for _, representative := range representatives {
		representativeBySource[representative.SourceID] = representative
	}

	items := make([]announcementRecord, 0, len(groups))
	for _, group := range groups {
		if representative, ok := representativeBySource[group.SourceID]; ok {
			items = append(items, announcementRecord{
				Notification: representative,
				PublishedAt:  publishedAtBySource[group.SourceID],
				Delivered:    countBySource[group.SourceID],
			})
		}
	}
	return items, total, nil
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
