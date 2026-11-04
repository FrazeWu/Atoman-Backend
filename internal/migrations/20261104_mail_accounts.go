package migrations

import (
	"fmt"

	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunMailAccountsMigration creates Atoman's invitation and mailbox-reservation
// tables. Mail contents and Stalwart's schema remain in the separate mail DB.
func RunMailAccountsMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.MailAccount{}, &model.MailInvite{}, &model.MailAccessCredential{}); err != nil {
		return fmt.Errorf("create mail accounts and invitations: %w", err)
	}
	if db.Dialector.Name() == "postgres" {
		// This is a fail-closed guard against a future bug accidentally treating
		// an unprovisioned account as an active mailbox.
		if err := db.Exec(`DO $$ BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'ck_mail_accounts_valid_status'
				AND conrelid = 'mail_accounts'::regclass
			) THEN
				ALTER TABLE mail_accounts ADD CONSTRAINT ck_mail_accounts_valid_status
				CHECK (status IN ('reserved', 'provisioning', 'active', 'suspended', 'failed'));
			END IF;
		END $$;`).Error; err != nil {
			return fmt.Errorf("enforce mail account lifecycle status: %w", err)
		}
	}
	return nil
}
