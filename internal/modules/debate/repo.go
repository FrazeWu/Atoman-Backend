package debate

import (
	"atoman/internal/model"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) CreateDebate(debate *model.Debate) error { return r.db.Create(debate).Error }

func (r *Repo) GetDebate(id uuid.UUID) (model.Debate, error) {
	var debate model.Debate
	err := r.db.Preload("User").First(&debate, "id = ?", id).Error
	return debate, err
}

func (r *Repo) CreateArgument(argument *model.Argument) error { return r.db.Create(argument).Error }

func (r *Repo) SaveDebate(debate *model.Debate) error { return r.db.Save(debate).Error }

func (r *Repo) DeleteDebate(id uuid.UUID) error {
	return r.db.Delete(&model.Debate{}, "id = ?", id).Error
}

func (r *Repo) GetArgument(id uuid.UUID) (model.Argument, error) {
	var argument model.Argument
	err := r.db.Preload("User").First(&argument, "id = ?", id).Error
	return argument, err
}

func (r *Repo) SaveArgument(argument *model.Argument) error { return r.db.Save(argument).Error }

func (r *Repo) DeleteArgument(id uuid.UUID) error {
	return r.db.Delete(&model.Argument{}, "id = ?", id).Error
}

func (r *Repo) ListDebates(query ListDebatesQuery) ([]model.Debate, int64, error) {
	db := r.db.Model(&model.Debate{})
	if status := strings.TrimSpace(query.Status); status != "" {
		db = db.Where("status = ?", status)
	}
	if search := strings.TrimSpace(query.Search); search != "" {
		db = db.Where("title LIKE ? OR description LIKE ? OR content LIKE ?", "%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	var debates []model.Debate
	err := db.Preload("User").Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&debates).Error
	return debates, total, err
}

func (r *Repo) ListArguments(debateID uuid.UUID) ([]model.Argument, error) {
	var arguments []model.Argument
	err := r.db.Preload("User").Where("debate_id = ?", debateID).Order("created_at ASC").Find(&arguments).Error
	return arguments, err
}
