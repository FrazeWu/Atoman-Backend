package migrations

import (
	"atoman/internal/model"
	"gorm.io/gorm"
)

func RunMailWorkspaceMigration(db *gorm.DB) error {
	return db.AutoMigrate(&model.MailDraft{}, &model.MailUpload{}, &model.MailPreference{}, &model.MailActionReceipt{}, &model.MailRetention{}, &model.MailContact{}, &model.MailExternalAccount{}, &model.MailOrganizerRecord{}, &model.MailOrganizerLabel{}, &model.MailExternalImportJob{}, &model.MailExternalImportItem{}, &model.MailFollowUpSchedule{}, &model.MailFollowUpBackfillState{}, &model.MailAlias{})
}
