package mail

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMailOutboxRuntimeFailsClosedWithoutOperationalAttestation(t *testing.T) {
	t.Setenv("ATOMAN_MAIL_SEND_ENABLED", "")
	service, err := NewConfiguredMailOutbox(nil)
	require.NoError(t, err)
	require.Nil(t, service)
	t.Setenv("ATOMAN_MAIL_SEND_ENABLED", "1")
	t.Setenv("ATOMAN_MAIL_R2_RW_VERIFIED", "")
	t.Setenv("ATOMAN_MAIL_TRUENAS_RESTORE_VERIFIED", "")
	t.Setenv("ATOMAN_MAIL_STALWART_READY", "")
	service, err = NewConfiguredMailOutbox(nil)
	require.Error(t, err)
	require.Nil(t, service)
	t.Setenv("ATOMAN_MAIL_R2_RW_VERIFIED", "1")
	t.Setenv("ATOMAN_MAIL_TRUENAS_RESTORE_VERIFIED", "1")
	t.Setenv("ATOMAN_MAIL_STALWART_READY", "1")
	service, err = NewConfiguredMailOutbox(nil)
	require.Error(t, err)
	require.Nil(t, service)
}
