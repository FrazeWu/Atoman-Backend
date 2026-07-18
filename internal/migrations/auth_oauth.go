package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunAuthOAuthMigration(db *gorm.DB) error {
	return db.AutoMigrate(&model.ExternalIdentity{}, &model.OAuthFlow{})
}
