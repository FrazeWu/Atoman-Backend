package forum

import (
	"encoding/json"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) ListCategories() ([]model.ForumCategory, error) {
	var categories []model.ForumCategory
	err := r.db.Order("name ASC").Find(&categories).Error
	return categories, err
}

func (r *Repo) GetCategory(id uuid.UUID) (model.ForumCategory, error) {
	var category model.ForumCategory
	err := r.db.First(&category, "id = ?", id).Error
	return category, err
}

func (r *Repo) CreateTopic(topic *model.ForumTopic) error { return r.db.Create(topic).Error }

func (r *Repo) GetTopic(id uuid.UUID) (model.ForumTopic, error) {
	var topic model.ForumTopic
	err := r.db.Preload("User").Preload("Category").First(&topic, "id = ?", id).Error
	return topic, err
}

func (r *Repo) GetTopicForUpdate(id uuid.UUID) (model.ForumTopic, error) {
	var topic model.ForumTopic
	err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("User").Preload("Category").First(&topic, "id = ?", id).Error
	return topic, err
}

func (r *Repo) ListTopics(query ListTopicsQuery) ([]model.ForumTopic, int64, error) {
	db := r.db.Model(&model.ForumTopic{})
	if query.CategoryID != uuid.Nil {
		db = db.Where("category_id = ?", query.CategoryID)
	}
	if search := strings.TrimSpace(query.Search); search != "" {
		db = db.Where("(title LIKE ? OR content LIKE ?)", "%"+search+"%", "%"+search+"%")
	}
	if tag := strings.TrimSpace(query.Tag); tag != "" {
		encoded, _ := json.Marshal(tag)
		db = db.Where("tags LIKE ? ESCAPE '\\'", "%"+escapeLike(string(encoded))+"%")
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var topics []model.ForumTopic
	err := db.Preload("User").Preload("Category").Order(topicOrder(query.Sort)).Offset(offset(query.Page, query.PageSize)).Limit(normalizedPageSize(query.PageSize)).Find(&topics).Error
	return topics, total, err
}

func (r *Repo) SaveTopic(topic *model.ForumTopic) error { return r.db.Save(topic).Error }

func (r *Repo) DeleteTopic(id uuid.UUID) error {
	return r.db.Delete(&model.ForumTopic{}, "id = ?", id).Error
}

func (r *Repo) CreateReply(reply *model.ForumReply) error { return r.db.Create(reply).Error }

func (r *Repo) GetReply(id uuid.UUID) (model.ForumReply, error) {
	var reply model.ForumReply
	err := r.db.Preload("User").Preload("Topic").First(&reply, "id = ?", id).Error
	return reply, err
}

func (r *Repo) ListReplies(topicID uuid.UUID, sort string) ([]model.ForumReply, error) {
	var replies []model.ForumReply
	order := "floor_number ASC, created_at ASC"
	if sort == "best" {
		order = "like_count DESC, floor_number ASC"
	}
	err := r.db.Preload("User").Where("topic_id = ?", topicID).Order(order).Find(&replies).Error
	return replies, err
}

func (r *Repo) SaveReply(reply *model.ForumReply) error { return r.db.Save(reply).Error }

func (r *Repo) DeleteReply(id uuid.UUID) error {
	return r.db.Delete(&model.ForumReply{}, "id = ?", id).Error
}

func (r *Repo) CountReplies(topicID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.Model(&model.ForumReply{}).Where("topic_id = ?", topicID).Count(&count).Error
	return count, err
}

func (r *Repo) UpsertDraft(draft *model.ForumDraft) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "user_id"},
			{Name: "context_key"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"title", "content", "tags", "updated_at"}),
	}).Create(draft).Error
}

func (r *Repo) ListDrafts(userID uuid.UUID) ([]model.ForumDraft, error) {
	var drafts []model.ForumDraft
	err := r.db.Where("user_id = ?", userID).Order("updated_at DESC").Find(&drafts).Error
	return drafts, err
}

func (r *Repo) GetDraft(userID uuid.UUID, contextKey string) (model.ForumDraft, error) {
	var draft model.ForumDraft
	err := r.db.Where("user_id = ? AND context_key = ?", userID, contextKey).First(&draft).Error
	return draft, err
}

func (r *Repo) DeleteDraft(userID uuid.UUID, draftID uuid.UUID) error {
	return r.db.Where("user_id = ? AND id = ?", userID, draftID).Delete(&model.ForumDraft{}).Error
}

func (r *Repo) DeleteDraftByContext(userID uuid.UUID, contextKey string) error {
	return r.db.Where("user_id = ? AND context_key = ?", userID, contextKey).Delete(&model.ForumDraft{}).Error
}

func (r *Repo) UpsertFollow(follow *model.ForumFollow) error {
	if err := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "target_type"}, {Name: "target_key"}},
		DoNothing: true,
	}).Create(follow).Error; err != nil {
		return err
	}
	var stored model.ForumFollow
	lookup := r.db.Session(&gorm.Session{NewDB: true})
	if err := lookup.Where("user_id = ? AND target_type = ? AND target_key = ?", follow.UserID, follow.TargetType, follow.TargetKey).First(&stored).Error; err != nil {
		return err
	}
	*follow = stored
	return nil
}

func (r *Repo) ListFollows(userID uuid.UUID) ([]model.ForumFollow, error) {
	var follows []model.ForumFollow
	err := r.db.Where("user_id = ?", userID).Order("created_at ASC, id ASC").Find(&follows).Error
	return follows, err
}

func (r *Repo) DeleteFollow(userID uuid.UUID, targetType, targetKey string) error {
	return r.db.Unscoped().Where("user_id = ? AND target_type = ? AND target_key = ?", userID, targetType, targetKey).Delete(&model.ForumFollow{}).Error
}

func (r *Repo) ListFollowerIDs(targetType, targetKey string) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.db.Model(&model.ForumFollow{}).
		Where("target_type = ? AND target_key = ?", targetType, targetKey).
		Order("created_at ASC, id ASC").
		Pluck("user_id", &ids).Error
	return ids, err
}

func (r *Repo) TagExists(tag string) (bool, error) {
	encoded, _ := json.Marshal(tag)
	var count int64
	err := r.db.Model(&model.ForumTopic{}).Where("tags LIKE ? ESCAPE '\\'", "%"+escapeLike(string(encoded))+"%").Count(&count).Error
	return count > 0, err
}

func topicOrder(sort string) string {
	switch sort {
	case "top":
		return "like_count DESC, reply_count DESC, created_at DESC"
	case "active":
		return "COALESCE(last_reply_at, created_at) DESC"
	case "featured":
		return "featured DESC, created_at DESC"
	default:
		return "created_at DESC"
	}
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return replacer.Replace(value)
}

func offset(page int, pageSize int) int {
	page = normalizedPage(page)
	return (page - 1) * normalizedPageSize(pageSize)
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
