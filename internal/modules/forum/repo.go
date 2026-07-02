package forum

import (
	"atoman/internal/model"
	"strings"

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
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var topics []model.ForumTopic
	err := db.Preload("User").Preload("Category").Order("created_at DESC").Offset(offset(query.Page, query.PageSize)).Limit(normalizedPageSize(query.PageSize)).Find(&topics).Error
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

func (r *Repo) ListReplies(topicID uuid.UUID) ([]model.ForumReply, error) {
	var replies []model.ForumReply
	err := r.db.Preload("User").Where("topic_id = ?", topicID).Order("floor_number ASC, created_at ASC").Find(&replies).Error
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

func (r *Repo) DeleteDraft(userID uuid.UUID, draftID uuid.UUID) error {
	return r.db.Where("user_id = ? AND id = ?", userID, draftID).Delete(&model.ForumDraft{}).Error
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
