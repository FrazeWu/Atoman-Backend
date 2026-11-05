package migrations

import (
	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRunMailOutboxMigration(t *testing.T) {
	db := testdb.OpenPostgres(t, "mail_outbox")
	require.NoError(t, RunMailOutboxMigration(db))
	require.NoError(t, RunMailOutboxMigration(db))
	require.True(t, db.Migrator().HasTable(&model.MailPendingSend{}))
	for _, name := range []string{"idx_mail_pending_user_request", "idx_mail_pending_snapshot", "idx_mail_pending_due"} {
		require.True(t, db.Migrator().HasIndex(&model.MailPendingSend{}, name), name)
	}
	var count int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_constraint WHERE conname='ck_mail_pending_send_status' AND conrelid='mail_pending_sends'::regclass`).Scan(&count).Error)
	require.Equal(t, int64(1), count)
}
