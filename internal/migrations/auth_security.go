package migrations

import (
	"atoman/internal/model"
	"time"

	"gorm.io/gorm"
)

type authSessionSecurityColumns struct {
	UserAgent    *string    `gorm:"size:512"`
	IPPrefix     *string    `gorm:"size:64"`
	LastActiveAt *time.Time `gorm:"index"`
}

func (authSessionSecurityColumns) TableName() string {
	return "auth_sessions"
}

func RunAuthSecurityMigration(db *gorm.DB) error {
	if db.Migrator().HasTable(&model.AuthSession{}) {
		columns := &authSessionSecurityColumns{}
		for _, field := range []string{"UserAgent", "IPPrefix", "LastActiveAt"} {
			if !db.Migrator().HasColumn(&model.AuthSession{}, field) {
				if err := db.Migrator().AddColumn(columns, field); err != nil {
					return err
				}
			}
		}
		if err := db.Exec("UPDATE auth_sessions SET user_agent = '' WHERE user_agent IS NULL").Error; err != nil {
			return err
		}
		if err := db.Exec("UPDATE auth_sessions SET ip_prefix = '' WHERE ip_prefix IS NULL").Error; err != nil {
			return err
		}
		if err := db.Exec("UPDATE auth_sessions SET last_active_at = COALESCE(created_at, CURRENT_TIMESTAMP) WHERE last_active_at IS NULL").Error; err != nil {
			return err
		}
	}
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
