package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunAuthPasswordResetMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.EmailVerificationCode{}) {
		return db.AutoMigrate(&model.User{}, &model.EmailVerificationCode{})
	}

	if db.Migrator().HasIndex(&model.EmailVerificationCode{}, "idx_email_verification_codes_email") {
		if err := db.Migrator().DropIndex(&model.EmailVerificationCode{}, "idx_email_verification_codes_email"); err != nil {
			return err
		}
	}

	return db.AutoMigrate(&model.User{}, &model.EmailVerificationCode{})
}
