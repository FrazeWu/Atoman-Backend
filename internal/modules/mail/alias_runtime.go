package mail

import (
	"errors"
	"gorm.io/gorm"
	"os"
)

func NewConfiguredMailAliases(db *gorm.DB) (*MailAliasService, error) {
	if os.Getenv("ATOMAN_MAIL_ALIASES_ENABLED") != "1" {
		return nil, nil
	}
	provision, err := NewConfiguredMailProvision(db)
	if err != nil {
		return nil, err
	}
	if provision == nil || !provision.gates.ready() {
		return nil, errors.New("Mail aliases require approved provisioning gates")
	}
	client, ok := provision.provider.(*StalwartJMAPClient)
	if !ok {
		return nil, errors.New("Mail alias provisioner unavailable")
	}
	if !db.Migrator().HasTable("mail_aliases") {
		return nil, errors.New("Mail aliases migration required")
	}
	return NewMailAliasService(db, client, client, provision.admin, provision.gates), nil
}
