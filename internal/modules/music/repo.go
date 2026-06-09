package music

import (
	"errors"

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
