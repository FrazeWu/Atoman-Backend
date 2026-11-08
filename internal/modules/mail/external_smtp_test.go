package mail

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"atoman/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeExternalSMTPRemote struct {
	Calls    int
	Provider string
	Address  string
	Password string
	Payload  outboxPayload
	Err      error
}

func (s *fakeExternalSMTPRemote) Send(_ context.Context, provider, address, password string, p outboxPayload) error {
	s.Calls++
	s.Provider = provider
	s.Address = address
	s.Password = password
	s.Payload = p
	return s.Err
}

func createOutboxExternal(t *testing.T, s *MailOutboxService, owner uuid.UUID, provider, address string) uuid.UUID {
	t.Helper()
	require.NoError(t, s.db.AutoMigrate(&model.MailExternalAccount{}))
	id := uuid.New()
	raw, err := json.Marshal(externalSecrets{Address: address, AppPassword: "provider-app-password-123"})
	require.NoError(t, err)
	secret, err := s.vault.Seal(owner, id, raw)
	require.NoError(t, err)
	row := model.MailExternalAccount{ID: id, UserID: owner, Provider: provider, CredentialCiphertext: secret, Revision: 1}
	require.NoError(t, s.db.Create(&row).Error)
	return id
}

func TestExternalSMTPQueueReauthorizesSenderAndPersistsImmutableOrigin(t *testing.T) {
	db, s, storage, _, alice, bob, _ := mailOutboxFixture(t)
	ctx := context.Background()
	gmailID := createOutboxExternal(t, s, alice.ID, "gmail", "alice@gmail.com")
	input := composeFixture()
	input.ExternalAccountID = &gmailID
	_, err := s.Queue(ctx, alice, input)
	assertAppCode(t, err, "mail.external_send_disabled")
	s.EnableExternalSMTP(true)
	result, err := s.Queue(ctx, bob, input)
	require.Error(t, err)
	// Bob may not borrow Alice's Gmail token, even with the real UUID.
	result, err = s.Queue(ctx, alice, input)
	require.NoError(t, err)
	require.Equal(t, "queued", result.Status)
	var stored model.MailPendingSend
	require.NoError(t, db.Where("id=? AND user_id=?", result.ID, alice.ID).Take(&stored).Error)
	require.NotNil(t, stored.ExternalAccountID)
	require.Equal(t, gmailID, *stored.ExternalAccountID)
	encrypted, err := storage.Get(ctx, stored.SnapshotKey)
	require.NoError(t, err)
	opened, err := s.vault.Open(alice.ID, stored.ID, encrypted)
	require.NoError(t, err)
	var payload outboxPayload
	require.NoError(t, json.Unmarshal(opened, &payload))
	require.Equal(t, "alice@gmail.com", payload.Sender)
	require.Equal(t, gmailID, *payload.ExternalAccountID)
	require.Contains(t, string(payload.MIME), "From: alice@gmail.com\r\n")
	require.NotContains(t, string(payload.MIME), "Bcc:")
	require.NotContains(t, string(encrypted), "provider-app-password-123")
	previous, err := s.Queue(ctx, alice, input)
	require.NoError(t, err)
	require.Equal(t, result.ID, previous.ID)
	forgedID := uuid.New()
	input.ExternalAccountID = &forgedID
	_, err = s.Queue(ctx, alice, input)
	assertAppCode(t, err, "mail.idempotency_conflict")
	input.RequestID = uuid.New()
	_, err = s.Queue(ctx, alice, input)
	assertAppCode(t, err, "mail.external_account_missing")
}

func TestExternalSMTPSubmitOnlyUsesCurrentOwnedEncryptedCredential(t *testing.T) {
	db, s, storage, _, alice, _, _ := mailOutboxFixture(t)
	ctx := context.Background()
	externalID := createOutboxExternal(t, s, alice.ID, "icloud", "alice@icloud.com")
	s.EnableExternalSMTP(true)
	input := composeFixture()
	input.ExternalAccountID = &externalID
	queued, err := s.Queue(ctx, alice, input)
	require.NoError(t, err)
	var pending model.MailPendingSend
	require.NoError(t, db.Where("id=?", queued.ID).Take(&pending).Error)
	snapshot, err := storage.Get(ctx, pending.SnapshotKey)
	require.NoError(t, err)
	raw, err := s.vault.Open(alice.ID, pending.ID, snapshot)
	require.NoError(t, err)
	var payload outboxPayload
	require.NoError(t, json.Unmarshal(raw, &payload))
	remote := &fakeExternalSMTPRemote{}
	submitter := NewStalwartOutboxSubmitter(db, &StalwartJMAPClient{}, fixtureCredentialVault(t))
	submitter.EnableExternalSMTP(s.vault, remote)
	require.NoError(t, db.Model(&pending).Update("status", "dispatching").Error)
	require.NoError(t, submitter.Submit(ctx, alice.ID, pending.AccountID, pending.ID, payload))
	require.Equal(t, 1, remote.Calls)
	require.Equal(t, "icloud", remote.Provider)
	require.Equal(t, "alice@icloud.com", remote.Address)
	require.Equal(t, "provider-app-password-123", remote.Password)
	require.Equal(t, "alice@icloud.com", remote.Payload.Sender)
	payload.Sender = "someone@example.test"
	require.Error(t, submitter.Submit(ctx, alice.ID, pending.AccountID, pending.ID, payload))
	require.Equal(t, 1, remote.Calls)
	payload.Sender = "alice@icloud.com"
	payload.MIME = []byte("From: impersonate@example.test\r\nTo: target@example.test\r\n\r\nSpoof")
	require.Error(t, submitter.Submit(ctx, alice.ID, pending.AccountID, pending.ID, payload))
	require.Equal(t, 1, remote.Calls)
	remote.Err = errors.New("SMTP confirmation lost")
	payload.MIME = []byte("From: alice@icloud.com\r\nTo: target@example.test\r\n\r\nPrivate")
	require.Error(t, submitter.Submit(ctx, alice.ID, pending.AccountID, pending.ID, payload))
	require.Equal(t, 2, remote.Calls)
	// Removing a connected mailbox after snapshot creation always fails closed.
	require.NoError(t, db.Where("id=? AND user_id=?", externalID, alice.ID).Delete(&model.MailExternalAccount{}).Error)
	require.Error(t, submitter.Submit(ctx, alice.ID, pending.AccountID, pending.ID, payload))
	require.Equal(t, 2, remote.Calls)
}

func TestExternalSMTPSendersScopedAndFeatureGated(t *testing.T) {
	_, s, _, _, alice, bob, _ := mailOutboxFixture(t)
	ctx := context.Background()
	_ = createOutboxExternal(t, s, alice.ID, "qq", "alice@qq.com")
	senders, err := s.Senders(ctx, alice)
	require.NoError(t, err)
	require.Len(t, senders, 1)
	require.Equal(t, "atoman", senders[0].Provider)
	s.EnableExternalSMTP(true)
	senders, err = s.Senders(ctx, alice)
	require.NoError(t, err)
	require.Len(t, senders, 2)
	require.Equal(t, "alice@qq.com", senders[1].Address)
	require.Nil(t, senders[0].ExternalAccountID)
	require.NotNil(t, senders[1].ExternalAccountID)
	senders, err = s.Senders(ctx, bob)
	// Bob's native address may be unprovisioned; neither case exposes Alice's.
	if err == nil {
		for _, item := range senders {
			require.NotEqual(t, "alice@qq.com", item.Address)
		}
	}
	_, err = openExternalSMTPSecrets(s.vault, model.MailExternalAccount{ID: uuid.New(), UserID: uuid.New(), Provider: "forged", CredentialCiphertext: []byte("fake")})
	require.Error(t, err)
}
