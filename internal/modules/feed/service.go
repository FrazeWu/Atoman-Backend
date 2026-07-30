package feed

import (
	"atoman/internal/model"
	legacyfeed "atoman/internal/service"

	"gorm.io/gorm"
)

type Service struct {
	db         *gorm.DB
	repo       *Repo
	syncSource func(*gorm.DB, model.FeedSource) (legacyfeed.RSSSyncResult, error)
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db, repo: NewRepo(db), syncSource: legacyfeed.SyncSingleRSSWithResult}
}
