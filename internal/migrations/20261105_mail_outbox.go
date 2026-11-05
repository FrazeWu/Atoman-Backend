package migrations

import (
	"atoman/internal/model"
	"fmt"
	"gorm.io/gorm"
)

// RunMailOutboxMigration is additive; it never modifies Stalwart's DB.
func RunMailOutboxMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.MailPendingSend{}); err != nil {
		return fmt.Errorf("create mail pending send table: %w", err)
	}
	if db.Dialector.Name() == "postgres" {
		if err := db.Exec(`DO $$ BEGIN
   IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='ck_mail_pending_send_status' AND conrelid='mail_pending_sends'::regclass) THEN
    ALTER TABLE mail_pending_sends ADD CONSTRAINT ck_mail_pending_send_status CHECK (status IN ('queued','dispatching','cancelled','sent','uncertain','blocked'));
   END IF;
  END $$;`).Error; err != nil {
			return fmt.Errorf("enforce pending send lifecycle: %w", err)
		}
	}
	return nil
}
