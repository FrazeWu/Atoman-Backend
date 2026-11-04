package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRunMailAccountsMigration(t *testing.T) {
	db := testdb.OpenPostgres(t, "mail_migration")
	require.NoError(t, RunMailAccountsMigration(db))
	require.NoError(t, RunMailAccountsMigration(db))
	for _, table := range []any{&model.MailAccount{}, &model.MailInvite{}, &model.MailAccessCredential{}} {
		require.True(t, db.Migrator().HasTable(table))
	}
	for _, idx := range []string{"idx_mail_accounts_user", "idx_mail_accounts_local", "idx_mail_accounts_address", "idx_mail_accounts_stalwart"} {
		require.True(t, db.Migrator().HasIndex(&model.MailAccount{}, idx), idx)
	}
	require.True(t, db.Migrator().HasIndex(&model.MailInvite{}, "idx_mail_invites_token_hash"))
	require.True(t, db.Migrator().HasIndex(&model.MailAccessCredential{}, "idx_mail_access_user"))

	account := model.MailAccount{ID: uuid.New(), UserID: uuid.New(), LocalPart: "legit", Address: "legit@atoman.org", Status: "reserved"}
	require.NoError(t, db.Create(&account).Error)
	require.Error(t, db.Model(&account).Update("status", "unprovisioned-but-active").Error)
}
