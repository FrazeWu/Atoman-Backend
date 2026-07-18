package studio

import (
	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repo struct {
	db *gorm.DB
}

func NewRepo(db *gorm.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) ListOwnedChannels(userID uuid.UUID) ([]model.Channel, error) {
	var channels []model.Channel
	err := r.db.Where("user_id = ?", userID).Order("created_at ASC, id ASC").Find(&channels).Error
	return channels, err
}

func (r *Repo) GetChannel(id uuid.UUID) (model.Channel, error) {
	var channel model.Channel
	err := r.db.First(&channel, "id = ?", id).Error
	return channel, err
}

func (r *Repo) GetState(userID uuid.UUID) (model.UserStudioState, error) {
	var state model.UserStudioState
	err := r.db.First(&state, "user_id = ?", userID).Error
	return state, err
}

func (r *Repo) ListCollections(channelID uuid.UUID, module Module) ([]model.Collection, error) {
	var collections []model.Collection
	err := r.db.Where("channel_id = ? AND content_type = ?", channelID, module).
		Order("is_default DESC, created_at ASC, id ASC").Find(&collections).Error
	return collections, err
}

func (r *Repo) GetCollection(id uuid.UUID) (model.Collection, error) {
	var collection model.Collection
	err := r.db.First(&collection, "id = ?", id).Error
	return collection, err
}
