package debate

import (
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func escapeDebateLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func (r *Repo) GetDebate(id uuid.UUID) (model.Debate, error) {
	var debate model.Debate
	err := r.db.Preload("User").First(&debate, "debates.id = ?", id).Error
	return debate, err
}

func (r *Repo) ListDebates(query ListDebatesQuery) ([]model.Debate, int64, error) {
	db := r.db.Model(&model.Debate{})
	if status := strings.TrimSpace(query.Status); status != "" {
		db = db.Where("status = ?", status)
	}
	if search := strings.TrimSpace(query.Search); search != "" {
		pattern := "%" + escapeDebateLike(strings.ToLower(search)) + "%"
		db = db.Where(`LOWER(title) LIKE ? ESCAPE '\' OR LOWER(description) LIKE ? ESCAPE '\' OR LOWER(content) LIKE ? ESCAPE '\'`, pattern, pattern, pattern)
	}
	if tag := strings.TrimSpace(query.Tag); tag != "" {
		if r.db.Dialector.Name() == "postgres" || r.db.Dialector.Name() == "pgx" {
			db = db.Where("tags @> ARRAY[?]::text[]", tag)
		} else {
			db = db.Where("tags LIKE ?", "%"+escapeDebateLike(tag)+"%")
		}
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize := query.Page, query.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	var debates []model.Debate
	err := db.Preload("User").Order("created_at DESC").Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&debates).Error
	return debates, total, err
}
