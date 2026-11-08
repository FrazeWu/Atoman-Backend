package mail

import (
	"atoman/internal/model"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeExternalIMAP struct {
	SyncCalls int
	ReadCalls int
	FlagCalls int
	FlagFail  bool
	LastSeen  bool
	Fail      bool
	Body      string
}

func (f *fakeExternalIMAP) Sync(ctx context.Context, provider, user, pass string) (ExternalInboxSnapshot, error) {
	f.SyncCalls++
	if f.Fail {
		return ExternalInboxSnapshot{}, context.DeadlineExceeded
	}
	if pass != "own-app-password" {
		return ExternalInboxSnapshot{}, context.Canceled
	}
	return ExternalInboxSnapshot{UIDValidity: 27, Messages: []ExternalMessageInfo{
		{UID: 123, Subject: "Private mail", From: "sender@example.org", Date: time.Now().Add(-time.Hour), Seen: false},
		{UID: 122, Subject: "Other mail", From: "second@example.org", Date: time.Now().Add(-2 * time.Hour), Seen: true},
	}, SyncedAt: time.Now().UTC()}, nil
}
func (f *fakeExternalIMAP) ReadPlain(ctx context.Context, provider, user, pass string, validity, uid uint32) (string, error) {
	f.ReadCalls++
	if pass != "own-app-password" || validity != 27 || uid != 123 {
		return "", context.Canceled
	}
	return "external private plain text", nil
}

func (f *fakeExternalIMAP) SetSeen(ctx context.Context, provider, user, pass string, validity, uid uint32, seen bool) error {
	f.FlagCalls++
	if f.FlagFail || pass != "own-app-password" || validity != 27 || uid != 123 {
		return context.DeadlineExceeded
	}
	f.LastSeen = seen
	return nil
}
func TestExternalSeenWriteScopedAndEncrypted(t *testing.T) {
	workspace, _, _, alice, bob := workspaceFixture(t, nil)
	fake := &fakeExternalIMAP{}
	svc := NewMailExternalService(workspace, fake)
	ctx := context.Background()
	account, err := svc.Connect(ctx, alice, ExternalConnectInput{Provider: "gmail", Address: "alice@gmail.com", AppPassword: "own-app-password"})
	require.NoError(t, err)
	_, err = svc.SetSeen(ctx, bob, account.ID, 123, 27, true)
	assertAppCode(t, err, "mail.external_account_missing")
	_, err = svc.SetSeen(ctx, alice, account.ID, 123, 26, true)
	assertAppCode(t, err, "mail.external_stale")
	_, err = svc.SetSeen(ctx, alice, account.ID, 999, 27, true)
	assertAppCode(t, err, "mail.external_message_missing")
	require.Equal(t, 0, fake.FlagCalls)
	fake.FlagFail = true
	_, err = svc.SetSeen(ctx, alice, account.ID, 123, 27, true)
	assertAppCode(t, err, "mail.external_flag_failed")
	again, err := svc.Accounts(ctx, alice)
	require.NoError(t, err)
	require.Equal(t, 1, again[0].UnreadCount)
	fake.FlagFail = false
	result, err := svc.SetSeen(ctx, alice, account.ID, 123, 27, true)
	require.NoError(t, err)
	require.Equal(t, 0, result.UnreadCount)
	require.True(t, fake.LastSeen)
	rows, err := svc.Unified(ctx, alice)
	require.NoError(t, err)
	require.True(t, rows.Messages[0].Seen)
	var persisted model.MailExternalAccount
	require.NoError(t, workspace.db.Where("id=?", account.ID).Take(&persisted).Error)
	require.NotContains(t, string(persisted.SnapshotCiphertext), "Private mail")
	_, err = svc.SetSeen(ctx, alice, account.ID, 123, 27, false)
	require.NoError(t, err)
	require.False(t, fake.LastSeen)
}
func TestExternalProviderAllowlistAndEmailValidation(t *testing.T) {
	for _, provider := range []string{"gmail", "qq", "163", "icloud", "yahoo"} {
		host, ok := externalProviderHost(provider)
		require.True(t, ok)
		require.True(t, strings.HasPrefix(host, "imap."))
	}
	for _, in := range []ExternalConnectInput{
		{Provider: "custom", Address: "user@example.org", AppPassword: "good-password"},
		{Provider: "gmail", Address: "user@example.org\r\nBcc:x@example.org", AppPassword: "good-password"},
		{Provider: "gmail", Address: "user@example.org", AppPassword: "x"},
		{Provider: "gmail", Address: "user@example.org", AppPassword: "good\npassword"},
	} {
		_, err := validateExternalConnect(in)
		require.Error(t, err)
	}
	good, err := validateExternalConnect(ExternalConnectInput{Provider: " GMAIL ", Address: " ALICE@GMAIL.COM ", AppPassword: "  own-app-password  "})
	require.NoError(t, err)
	require.Equal(t, "gmail", good.Provider)
	require.Equal(t, "alice@gmail.com", good.Address)
	require.Equal(t, "own-app-password", good.AppPassword)
}
func TestExternalPrivateAccountsScopedEncryptedAndReadOnly(t *testing.T) {
	workspace, _, _, alice, bob := workspaceFixture(t, nil)
	fake := &fakeExternalIMAP{}
	s := NewMailExternalService(workspace, fake)
	ctx := context.Background()
	account, err := s.Connect(ctx, alice, ExternalConnectInput{
		Provider: "gmail", Address: "alice@gmail.com", AppPassword: "own-app-password"})
	require.NoError(t, err)
	require.Equal(t, "alice@gmail.com", account.Address)
	require.Equal(t, 1, account.UnreadCount)
	require.Equal(t, 2, account.MessagesCount)
	require.Equal(t, 1, fake.SyncCalls)

	var persisted model.MailExternalAccount
	require.NoError(t, s.workspace.db.Where("id=?", account.ID).Take(&persisted).Error)
	require.NotContains(t, string(persisted.CredentialCiphertext), "gmail.com")
	require.NotContains(t, string(persisted.CredentialCiphertext), "own-app-password")
	require.NotContains(t, string(persisted.SnapshotCiphertext), "Private mail")
	require.NotContains(t, string(persisted.SnapshotCiphertext), "sender@example.org")
	require.Equal(t, uuid.UUID(alice.ID), persisted.UserID)

	listed, err := s.Accounts(ctx, alice)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	bobs, err := s.Accounts(ctx, bob)
	require.NoError(t, err)
	require.Empty(t, bobs)
	_, err = s.Connect(ctx, alice, ExternalConnectInput{Provider: "icloud", Address: "alice@gmail.com", AppPassword: "own-app-password"})
	assertAppCode(t, err, "mail.external_duplicate")
	_, err = s.Sync(ctx, bob, account.ID)
	assertAppCode(t, err, "mail.external_account_missing")
	_, err = s.Read(ctx, bob, account.ID, 123, 27)
	assertAppCode(t, err, "mail.external_account_missing")
	err = s.Delete(ctx, bob, account.ID)
	assertAppCode(t, err, "mail.external_account_missing")

	inbox, err := s.Unified(ctx, alice)
	require.NoError(t, err)
	require.Len(t, inbox.Messages, 2)
	require.Equal(t, account.ID, inbox.Messages[0].AccountID)
	require.Equal(t, uint32(123), inbox.Messages[0].UID)
	raw, _ := json.Marshal(inbox)
	require.NotContains(t, string(raw), "own-app-password")
	text, err := s.Read(ctx, alice, account.ID, 123, 27)
	require.NoError(t, err)
	require.Equal(t, "external private plain text", text)
	require.Equal(t, 1, fake.ReadCalls)
	_, err = s.Read(ctx, alice, account.ID, 123, 26)
	assertAppCode(t, err, "mail.external_stale")
	_, err = s.Read(ctx, alice, account.ID, 124, 27)
	assertAppCode(t, err, "mail.external_message_missing")
	require.Equal(t, 1, fake.ReadCalls)
	require.NoError(t, s.Delete(ctx, alice, account.ID))
	gone, err := s.Accounts(ctx, alice)
	require.NoError(t, err)
	require.Empty(t, gone)
}
func TestExternalFailedSyncKeepsCachedPrivateInbox(t *testing.T) {
	workspace, _, _, alice, _ := workspaceFixture(t, nil)
	fake := &fakeExternalIMAP{}
	s := NewMailExternalService(workspace, fake)
	ctx := context.Background()
	a, err := s.Connect(ctx, alice, ExternalConnectInput{Provider: "gmail", Address: "alice@gmail.com", AppPassword: "own-app-password"})
	require.NoError(t, err)
	fake.Fail = true
	_, err = s.Sync(ctx, alice, a.ID)
	assertAppCode(t, err, "mail.external_sync_failed")
	combined, err := s.Unified(ctx, alice)
	require.NoError(t, err)
	require.Len(t, combined.Messages, 2)
	require.Equal(t, "connection_failed", combined.Accounts[0].LastErrorCode)
}
func TestExternalMIMEPlaintextNeverRendersHTML(t *testing.T) {
	plain := "From: example@example.org\r\nSubject: sample\r\nContent-Type: text/html\r\n\r\n<script>alert(1)</script>"
	require.NotContains(t, externalPlainFromMIME([]byte(plain)), "<script>")
	multipart := "MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nSafe=20plain\r\n--x\r\nContent-Type: text/html\r\n\r\n<script>alert(1)</script>\r\n--x--\r\n"
	require.Equal(t, "Safe plain", externalPlainFromMIME([]byte(multipart)))
}
