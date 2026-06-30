package music

import (
	"errors"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) CreateEdit(edit *model.MusicEdit) error { return r.db.Create(edit).Error }

func (r *Repo) GetEdit(id uuid.UUID) (model.MusicEdit, error) {
	var edit model.MusicEdit
	err := r.db.First(&edit, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.MusicEdit{}, err
	}
	return edit, err
}

func (r *Repo) SaveEdit(edit *model.MusicEdit) error { return r.db.Save(edit).Error }

func (r *Repo) ClaimOpenEdit(id uuid.UUID, status string) (bool, error) {
	result := r.db.Model(&model.MusicEdit{}).
		Where("id = ? AND status = ?", id, "open").
		Update("status", status)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

type ListEditsQuery struct {
	Status      string
	EntityType  string
	Type        string
	SubmittedBy *uuid.UUID
	Page        int
	PageSize    int
}

func (r *Repo) ListEdits(query ListEditsQuery) ([]model.MusicEdit, int64, error) {
	db := r.db.Model(&model.MusicEdit{})
	if status := strings.TrimSpace(query.Status); status != "" {
		db = db.Where("status = ?", status)
	}
	if entityType := strings.TrimSpace(query.EntityType); entityType != "" {
		db = db.Where("entity_type = ?", entityType)
	}
	if editType := strings.TrimSpace(query.Type); editType != "" {
		db = db.Where("type = ?", editType)
	}
	if query.SubmittedBy != nil && *query.SubmittedBy != uuid.Nil {
		db = db.Where("submitted_by = ?", *query.SubmittedBy)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var edits []model.MusicEdit
	err := db.Order("created_at DESC").Limit(query.PageSize).Offset((query.Page - 1) * query.PageSize).Find(&edits).Error
	return edits, total, err
}
