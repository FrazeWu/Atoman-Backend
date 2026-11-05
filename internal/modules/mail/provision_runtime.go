package mail

import (
	"errors"
	"gorm.io/gorm"
	"os"
)

// NewConfiguredMailProvision uses a server-only administrator credential. It
// never accepts an administrator credential or target account from the browser.
func NewConfiguredMailProvision(db *gorm.DB) (*MailProvisionService, error) {
	if os.Getenv("ATOMAN_MAIL_PROVISION_ENABLED") != "1" {
		return nil, nil
	}
	gates := MailActivationGates{ProvisioningAllowed: os.Getenv("ATOMAN_MAIL_STALWART_READY") == "1", R2ReadWriteVerified: os.Getenv("ATOMAN_MAIL_R2_RW_VERIFIED") == "1", TrueNASRestoreVerified: os.Getenv("ATOMAN_MAIL_TRUENAS_RESTORE_VERIFIED") == "1"}
	if !gates.ready() {
		return nil, errors.New("Mail provisioning operational gates are not verified")
	}
	client, e := NewStalwartJMAPClient(os.Getenv("ATOMAN_MAIL_STALWART_URL"), nil)
	if e != nil {
		return nil, e
	}
	vault, e := NewMailCredentialVault(os.Getenv("ATOMAN_MAIL_JMAP_ENCRYPTION_KEY_B64"))
	if e != nil {
		return nil, e
	}
	admin := StalwartBasic(os.Getenv("ATOMAN_MAIL_STALWART_ADMIN_USER"), os.Getenv("ATOMAN_MAIL_STALWART_ADMIN_PASSWORD"))
	if _, e = admin.authorization(); e != nil {
		return nil, errors.New("Mail administrator credential is unavailable")
	}
	return NewMailProvisionService(db, client, vault, admin, gates), nil
}
