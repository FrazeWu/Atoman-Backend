package mail

import (
	"atoman/internal/model"
	"errors"
	"os"

	"gorm.io/gorm"
)

// NewConfiguredMailOutbox strictly fails closed. Readiness flags must only be
// asserted AFTER a human-reviewed, dated R2+TrueNAS+Stalwart recovery proof;
// they are not set or auto-derived by the application.
func NewConfiguredMailOutbox(db *gorm.DB) (*MailOutboxService, error) {
	if os.Getenv("ATOMAN_MAIL_SEND_ENABLED") != "1" {
		return nil, nil
	}
	if os.Getenv("ATOMAN_MAIL_R2_RW_VERIFIED") != "1" || os.Getenv("ATOMAN_MAIL_TRUENAS_RESTORE_VERIFIED") != "1" || os.Getenv("ATOMAN_MAIL_STALWART_READY") != "1" {
		return nil, errors.New("Mail sending is disabled until storage, recovery and Stalwart approval")
	}
	if db == nil {
		return nil, errors.New("Mail sending database unavailable")
	}
	r2, e := LoadR2ConfigFromEnv()
	if e != nil {
		return nil, errors.New("Mail storage configuration unavailable")
	}
	privateStore, e := NewR2MailPendingStore(r2)
	if e != nil {
		return nil, errors.New("Mail private object store unavailable")
	}
	snapshotVault, e := NewMailOutboxVault(os.Getenv("ATOMAN_MAIL_OUTBOX_ENCRYPTION_KEY_B64"))
	if e != nil {
		return nil, errors.New("Mail queue encryption key unavailable")
	}
	keyVault, e := NewMailCredentialVault(os.Getenv("ATOMAN_MAIL_JMAP_ENCRYPTION_KEY_B64"))
	if e != nil {
		return nil, errors.New("Mail user credential vault unavailable")
	}
	jmap, e := NewStalwartJMAPClient(os.Getenv("ATOMAN_MAIL_STALWART_URL"), nil)
	if e != nil {
		return nil, errors.New("Mail upstream JMAP URL unavailable")
	}
	if !db.Migrator().HasTable("mail_pending_sends") {
		return nil, errors.New("Mail sending database migration is missing")
	}
	transport := NewStalwartOutboxSubmitter(db, jmap, keyVault)
	externalEnabled := os.Getenv("ATOMAN_MAIL_EXTERNAL_SMTP_ENABLED") == "1"
	if externalEnabled {
		if !db.Migrator().HasTable(&model.MailExternalAccount{}) {
			return nil, errors.New("External SMTP account migration required")
		}
		transport.EnableExternalSMTP(snapshotVault, liveExternalSMTPRemote{})
	}
	outbox := NewMailOutboxService(db, privateStore, snapshotVault, transport, true, MailActivationGates{ProvisioningAllowed: true, R2ReadWriteVerified: true, TrueNASRestoreVerified: true})
	outbox.EnableExternalSMTP(externalEnabled)
	aliasEnabled := os.Getenv("ATOMAN_MAIL_ALIASES_ENABLED") == "1"
	if aliasEnabled && !db.Migrator().HasTable(&model.MailAlias{}) {
		return nil, errors.New("Mail aliases migration required")
	}
	outbox.EnableMailAliases(aliasEnabled)
	return outbox, nil
}
