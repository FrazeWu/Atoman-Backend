package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fakePendingStorage struct {
	mu       sync.Mutex
	items    map[string][]byte
	failRead bool
}

func newFakePendingStorage() *fakePendingStorage {
	return &fakePendingStorage{items: map[string][]byte{}}
}
func (s *fakePendingStorage) Put(_ context.Context, k string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[k] = append([]byte(nil), v...)
	return nil
}
func (s *fakePendingStorage) Get(_ context.Context, k string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failRead {
		return nil, errors.New("synthetic outage")
	}
	v, ok := s.items[k]
	if !ok {
		return nil, errors.New("missing")
	}
	return append([]byte(nil), v...), nil
}
func (s *fakePendingStorage) Delete(_ context.Context, k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, k)
	return nil
}
func (s *fakePendingStorage) length() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.items) }

type fakeMailSubmitter struct {
	mu     sync.Mutex
	calls  int
	failed bool
	last   outboxPayload
}

func (d *fakeMailSubmitter) Submit(_ context.Context, _ uuid.UUID, account string, _ uuid.UUID, p outboxPayload) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	d.last = p
	if account == "" || d.failed {
		return errors.New("synthetic unknown SMTP outcome")
	}
	return nil
}
func (d *fakeMailSubmitter) count() int { d.mu.Lock(); defer d.mu.Unlock(); return d.calls }

func mailOutboxFixture(t *testing.T) (*gorm.DB, *MailOutboxService, *fakePendingStorage, *fakeMailSubmitter, authctx.CurrentUser, authctx.CurrentUser, *time.Time) {
	t.Helper()
	db, _, admin, alice, bob := mailProvisionTestSetup(t)
	_, err := NewMailProvisionService(db, &fakeMailProvisioner{}, fixtureCredentialVault(t), StalwartBasic("admin", "fake-admin"), MailActivationGates{true, true, true}).ProvisionReserved(context.Background(), admin, alice.ID)
	require.NoError(t, err)
	require.NoError(t, migrations.RunMailOutboxMigration(db))
	require.NoError(t, migrations.RunMailOutboxMigration(db))
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xa1}, 32))
	vault, err := NewMailOutboxVault(key)
	require.NoError(t, err)
	storage := newFakePendingStorage()
	sender := &fakeMailSubmitter{}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	service := NewMailOutboxService(db, storage, vault, sender, true, MailActivationGates{true, true, true})
	service.now = func() time.Time { return now }
	return db, service, storage, sender, alice, bob, &now
}
func fixtureCredentialVault(t *testing.T) *MailCredentialVault {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 32))
	v, err := NewMailCredentialVault(key)
	require.NoError(t, err)
	return v
}
func composeFixture() ComposeMailInput {
	return ComposeMailInput{RequestID: uuid.New(), To: []string{"target@example.test"}, Bcc: []string{"private@example.test"}, Subject: "Atoman test", Text: "Hello test message"}
}

func TestMailOutboxUndoWindowAndIdempotency(t *testing.T) {
	_, s, storage, send, alice, bob, clock := mailOutboxFixture(t)
	ctx := context.Background()
	input := composeFixture()
	queued, err := s.Queue(ctx, alice, input)
	require.NoError(t, err)
	require.Equal(t, "queued", queued.Status)
	require.Equal(t, clock.Add(5*time.Second), queued.SendAfter)
	require.Equal(t, 1, storage.length())
	repeated, err := s.Queue(ctx, alice, input)
	require.NoError(t, err)
	require.Equal(t, queued.ID, repeated.ID)
	require.Equal(t, 1, storage.length())
	different := input
	different.Text = "changed"
	_, err = s.Queue(ctx, alice, different)
	assertAppCode(t, err, "mail.idempotency_conflict")
	_, err = s.Status(ctx, bob, queued.ID)
	assertAppCode(t, err, "mail.outbox_not_found")
	_, err = s.Cancel(ctx, bob, queued.ID)
	assertAppCode(t, err, "mail.outbox_not_found")
	count, err := s.RunDue(ctx, 2)
	require.NoError(t, err)
	require.Zero(t, count)
	canceled, err := s.Cancel(ctx, alice, queued.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", canceled.Status)
	require.Equal(t, 0, storage.length())
	again, err := s.Cancel(ctx, alice, queued.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", again.Status)
	*clock = clock.Add(10 * time.Second)
	count, err = s.RunDue(ctx, 5)
	require.NoError(t, err)
	require.Zero(t, count)
	require.Equal(t, 0, send.count())
}

func TestMailOutboxSendsOnlyAfterDeadlineWithoutBccHeader(t *testing.T) {
	_, s, storage, send, alice, _, clock := mailOutboxFixture(t)
	ctx := context.Background()
	message := composeFixture()
	queued, err := s.Queue(ctx, alice, message)
	require.NoError(t, err)
	*clock = clock.Add(6 * time.Second)
	_, err = s.Cancel(ctx, alice, queued.ID)
	assertAppCode(t, err, "mail.undo_window_closed")
	processed, err := s.RunDue(ctx, 5)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	require.Equal(t, 1, send.count())
	require.Equal(t, 0, storage.length())
	confirmed, err := s.Status(ctx, alice, queued.ID)
	require.NoError(t, err)
	require.Equal(t, "sent", confirmed.Status)
	require.Contains(t, string(send.last.MIME), "Message-ID: <"+queued.ID.String()+"@atoman.org>")
	require.NotContains(t, string(send.last.MIME), "Bcc:")
	require.Equal(t, []string{"target@example.test", "private@example.test"}, send.last.Recipients)
	processed, err = s.RunDue(ctx, 2)
	require.NoError(t, err)
	require.Zero(t, processed)
	require.Equal(t, 1, send.count())
}

func TestMailOutboxUncertainRemoteOutcomeNeverBlindlyRetries(t *testing.T) {
	db, s, store, submit, alice, _, clock := mailOutboxFixture(t)
	ctx := context.Background()
	submit.failed = true
	queued, err := s.Queue(ctx, alice, composeFixture())
	require.NoError(t, err)
	*clock = clock.Add(6 * time.Second)
	count, err := s.RunDue(ctx, 1)
	require.Equal(t, 1, count)
	require.ErrorContains(t, err, "reconciliation")
	state, err := s.Status(ctx, alice, queued.ID)
	require.NoError(t, err)
	require.Equal(t, "uncertain", state.Status)
	require.Equal(t, 1, store.length(), "retain encrypted snapshot for manual reconciliation")
	require.Equal(t, 1, submit.count())
	count, err = s.RunDue(ctx, 1)
	require.NoError(t, err)
	require.Zero(t, count)
	require.Equal(t, 1, submit.count())
	var stored model.MailPendingSend
	require.NoError(t, db.Where("id=?", queued.ID).Take(&stored).Error)
	require.NotNil(t, stored.ClaimedAt)
}

func TestMailOutboxBrokenPendingObjectFailsClosed(t *testing.T) {
	_, s, store, submit, alice, _, clock := mailOutboxFixture(t)
	ctx := context.Background()
	queued, err := s.Queue(ctx, alice, composeFixture())
	require.NoError(t, err)
	store.failRead = true
	*clock = clock.Add(8 * time.Second)
	_, err = s.RunDue(ctx, 1)
	require.ErrorContains(t, err, "reconciliation")
	state, err := s.Status(ctx, alice, queued.ID)
	require.NoError(t, err)
	require.Equal(t, "uncertain", state.Status)
	require.Equal(t, 0, submit.count())
}

func TestMailOutboxWorkerClaimsOnlyOnceAcrossConcurrentWorkers(t *testing.T) {
	_, s, _, submit, alice, _, clock := mailOutboxFixture(t)
	ctx := context.Background()
	entry, err := s.Queue(ctx, alice, composeFixture())
	require.NoError(t, err)
	*clock = clock.Add(7 * time.Second)
	var group sync.WaitGroup
	var first, second int
	group.Add(2)
	go func() { defer group.Done(); first, _ = s.RunDue(ctx, 1) }()
	go func() { defer group.Done(); second, _ = s.RunDue(ctx, 1) }()
	group.Wait()
	require.Equal(t, 1, first+second)
	require.Equal(t, 1, submit.count())
	state, err := s.Status(ctx, alice, entry.ID)
	require.NoError(t, err)
	require.Equal(t, "sent", state.Status)
}

func TestMailOutboxFailsClosedWithoutOperationalGates(t *testing.T) {
	db, s, store, _, alice, _, _ := mailOutboxFixture(t)
	s.allowed = false
	_, err := s.Queue(context.Background(), alice, composeFixture())
	assertAppCode(t, err, "mail.send_not_ready")
	require.Equal(t, 0, store.length())
	s.allowed = true
	s.deliverer = nil
	_, err = s.ClaimDue(context.Background())
	require.Error(t, err)
	_, err = s.RunDue(context.Background(), 1)
	require.Error(t, err)
	require.True(t, db.Migrator().HasTable(&model.MailPendingSend{}))
}

func TestMailOutboxPayloadValidationAndVaultIsolation(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, 32))
	vault, err := NewMailOutboxVault(key)
	require.NoError(t, err)
	user, id := uuid.New(), uuid.New()
	blob, err := vault.Seal(user, id, []byte("secret synthetic MIME"))
	require.NoError(t, err)
	raw, err := vault.Open(user, id, blob)
	require.NoError(t, err)
	require.Equal(t, "secret synthetic MIME", string(raw))
	_, err = vault.Open(uuid.New(), id, blob)
	require.Error(t, err)
	blob[len(blob)-1] ^= 0xff
	_, err = vault.Open(user, id, blob)
	require.Error(t, err)
	c := composeFixture()
	c.Subject = "x\r\nBcc: leaked@example.test"
	_, err = buildOutboxPayload("alice@atoman.org", uuid.New(), c, time.Now())
	require.Error(t, err)
	c = composeFixture()
	c.To = []string{"attacker@example.test\r\nX-Evil: 1"}
	_, err = buildOutboxPayload("alice@atoman.org", uuid.New(), c, time.Now())
	require.Error(t, err)
	c = composeFixture()
	c.To = []string{"Person <target@example.test>"}
	_, err = buildOutboxPayload("alice@atoman.org", uuid.New(), c, time.Now())
	require.Error(t, err)
	c = composeFixture()
	c.Text = strings.Repeat("x", 300*1024)
	_, err = buildOutboxPayload("alice@atoman.org", uuid.New(), c, time.Now())
	require.Error(t, err)
}
