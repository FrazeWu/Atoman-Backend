package debate

import (
	"atoman/internal/model"

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
