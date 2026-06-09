package blog

import (
	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) GetChannel(id uuid.UUID) (model.Channel, error) {
	var channel model.Channel
	err := r.db.First(&channel, "id = ?", id).Error
	return channel, err
}

func (r *Repo) GetPost(id uuid.UUID) (model.Post, error) {
	var post model.Post
	err := r.db.First(&post, "id = ?", id).Error
	return post, err
}
