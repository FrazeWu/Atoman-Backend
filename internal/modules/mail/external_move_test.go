package mail

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExternalMoveUpdatesOnlyOwnedInbox(t *testing.T) {
	workspace, _, _, alice, bob := workspaceFixture(t, nil)
	fake := &fakeExternalIMAP{}
	svc := NewMailExternalService(workspace, fake)
	ctx := context.Background()
	account, err := svc.Connect(ctx, alice, ExternalConnectInput{Provider: "gmail", Address: "alice@gmail.com", AppPassword: "own-app-password"})
	require.NoError(t, err)
	_, err = svc.MoveFolderMessage(ctx, bob, account.ID, "INBOX", "Archive/2026", 123, 27)
	assertAppCode(t, err, "mail.external_account_missing")
	_, err = svc.MoveFolderMessage(ctx, alice, account.ID, "INBOX", "INBOX", 123, 27)
	assertAppCode(t, err, "mail.external_invalid_move")
	require.Zero(t, fake.MoveCalls)
	fake.MoveFail = true
	_, err = svc.MoveFolderMessage(ctx, alice, account.ID, "INBOX", "Archive/2026", 123, 27)
	assertAppCode(t, err, "mail.external_move_failed")
	fake.MoveFail = false
	result, err := svc.MoveFolderMessage(ctx, alice, account.ID, "INBOX", "Archive/2026", 123, 27)
	require.NoError(t, err)
	require.True(t, result.Moved)
	require.True(t, result.InboxCacheUpdated)
	inbox, err := svc.Unified(ctx, alice)
	require.NoError(t, err)
	require.Len(t, inbox.Messages, 1)
	require.Equal(t, uint32(122), inbox.Messages[0].UID)
}
