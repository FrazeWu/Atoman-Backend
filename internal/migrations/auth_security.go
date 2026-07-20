package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunAuthSecurityMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.AuthSession{}, &model.EmailVerificationCode{}); err != nil {
		return err
	}
	if err := db.Exec("UPDATE email_verification_codes SET used = ? WHERE LENGTH(code) <> 64", true).Error; err != nil {
		return err
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON "Users" (LOWER(username)) WHERE deleted_at IS NULL`).Error; err != nil {
		return err
	}
	return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_lower ON "Users" (LOWER(email)) WHERE deleted_at IS NULL`).Error
}
