package mail

import (
	"context"
	"errors"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeAliases struct {
	AddCount    int
	EnableCount int
	LastLocal   string
	LastEnabled bool
	FailAdd     bool
	FailUpdate  bool
}

func (f *fakeAliases) FindDomainID(context.Context, StalwartCredential, string) (string, error) {
	return "domain-stage", nil
}
func (f *fakeAliases) AddAlias(_ context.Context, _ StalwartCredential, accountID, domain, local string) (uint32, error) {
	f.AddCount++
	f.LastLocal = local
	if f.FailAdd {
		return 0, errors.New("upstream status unknown")
	}
	return uint32(f.AddCount - 1), nil
}
func (f *fakeAliases) SetAliasEnabled(_ context.Context, _ StalwartCredential, accountID, domain, local string, slot uint32, enabled bool) error {
	f.EnableCount++
	f.LastLocal = local
	f.LastEnabled = enabled
	if f.FailUpdate {
		return errors.New("upstream status unknown")
	}
	return nil
}
func aliasFixture(t *testing.T) (*MailAliasService, *MailOutboxService, *fakeAliases, authctx.CurrentUser, authctx.CurrentUser) {
	t.Helper()
	db, outbox, _, _, alice, bob, _ := mailOutboxFixture(t)
	require.NoError(t, db.AutoMigrate(&model.MailAlias{}))
	fake := &fakeAliases{}
	return NewMailAliasService(db, fake, fake, StalwartBasic("admin", "fake-admin"), MailActivationGates{true, true, true}), outbox, fake, alice, bob
}

func TestMailAliasesValidateOwnershipAndAddressReservations(t *testing.T) {
	service, _, remote, alice, bob := aliasFixture(t)
	ctx := context.Background()
	_, err := service.Create(ctx, alice, "postmaster")
	assertAppCode(t, err, "mail.reserved_address")
	_, err = service.Create(ctx, bob, "bob-alias")
	assertAppCode(t, err, "mail.mailbox_inactive")
	alias, err := service.Create(ctx, alice, "  my.alias  ")
	require.NoError(t, err)
	require.Equal(t, "my.alias@atoman.org", alias.Address)
	require.Equal(t, "active", alias.Status)
	require.Equal(t, 1, remote.AddCount)
	rows, err := service.List(ctx, alice)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	_, err = service.List(ctx, bob)
	assertAppCode(t, err, "mail.mailbox_inactive")
	_, err = service.Create(ctx, alice, "my.alias")
	assertAppCode(t, err, "mail.address_unavailable")
	require.Equal(t, 1, remote.AddCount)
	// A brand-new native address is prevented from taking an alias address.
	_, err = NewAccountService(service.db, true).ReserveSelf(authctx.CurrentUser{ID: alice.ID, Role: "admin"}, "my.alias")
	assertAppCode(t, err, "mail.address_unavailable")
	_, err = service.SetEnabled(ctx, bob, alias.ID, false)
	assertAppCode(t, err, "mail.mailbox_inactive")
}
func TestMailAliasesDisabledDoesNotFreeAddressAndCanReenable(t *testing.T) {
	service, outbox, remote, alice, _ := aliasFixture(t)
	ctx := context.Background()
	alias, err := service.Create(ctx, alice, "alternate")
	require.NoError(t, err)
	outbox.EnableMailAliases(true)
	senders, err := outbox.Senders(ctx, alice)
	require.NoError(t, err)
	require.Len(t, senders, 2)
	require.Equal(t, "alternate@atoman.org", senders[1].Address)
	input := composeFixture()
	input.AliasID = &alias.ID
	queued, err := outbox.Queue(ctx, alice, input)
	require.NoError(t, err)
	var pending model.MailPendingSend
	require.NoError(t, outbox.db.Where("id=?", queued.ID).Take(&pending).Error)
	require.NotNil(t, pending.AliasID)
	require.Equal(t, alias.ID, *pending.AliasID)
	_, err = service.SetEnabled(ctx, alice, alias.ID, false)
	assertAppCode(t, err, "mail.alias_pending_send")
	_, err = outbox.Cancel(ctx, alice, queued.ID)
	require.NoError(t, err)
	disabled, err := service.SetEnabled(ctx, alice, alias.ID, false)
	require.NoError(t, err)
	require.Equal(t, "disabled", disabled.Status)
	require.False(t, remote.LastEnabled)
	_, err = outbox.Queue(ctx, alice, ComposeMailInput{RequestID: uuid.New(), AliasID: &alias.ID, To: []string{"someone@example.test"}, Text: "hi"})
	assertAppCode(t, err, "mail.alias_missing")
	senders, err = outbox.Senders(ctx, alice)
	require.NoError(t, err)
	require.Len(t, senders, 1)
	enabled, err := service.SetEnabled(ctx, alice, alias.ID, true)
	require.NoError(t, err)
	require.Equal(t, "active", enabled.Status)
	require.True(t, remote.LastEnabled)
	require.Equal(t, 2, remote.EnableCount)
	_, err = service.Create(ctx, alice, "alternate")
	assertAppCode(t, err, "mail.address_unavailable")
}
func TestMailAliasesRemoteUnknownNeverAutomaticallyRetries(t *testing.T) {
	service, _, fake, alice, _ := aliasFixture(t)
	ctx := context.Background()
	fake.FailAdd = true
	_, err := service.Create(ctx, alice, "manual-check")
	assertAppCode(t, err, "mail.alias_review_required")
	require.Equal(t, 1, fake.AddCount)
	_, err = service.Create(ctx, alice, "manual-check")
	assertAppCode(t, err, "mail.address_unavailable")
	require.Equal(t, 1, fake.AddCount)
	var row model.MailAlias
	require.NoError(t, service.db.Where("user_id=?", alice.ID).Take(&row).Error)
	require.Equal(t, "needs_review", row.Status)
	_, err = service.SetEnabled(ctx, alice, row.ID, true)
	assertAppCode(t, err, "mail.alias_state_changed")
	fake.FailAdd = false
	a, err := service.Create(ctx, alice, "known-good")
	require.NoError(t, err)
	fake.FailUpdate = true
	_, err = service.SetEnabled(ctx, alice, a.ID, false)
	assertAppCode(t, err, "mail.alias_review_required")
	var updated model.MailAlias
	require.NoError(t, service.db.Where("id=?", a.ID).Take(&updated).Error)
	require.Equal(t, "needs_review", updated.Status)
	_, err = service.SetEnabled(ctx, alice, a.ID, false)
	assertAppCode(t, err, "mail.alias_state_changed")
	require.Equal(t, 1, fake.EnableCount)
}
func TestMailAliasesNativeOutboxRejectsOtherOwnerAndMixedIdentities(t *testing.T) {
	service, outbox, _, alice, bob := aliasFixture(t)
	alias, err := service.Create(context.Background(), alice, "ali-sender")
	require.NoError(t, err)
	external := createOutboxExternal(t, outbox, alice.ID, "gmail", "alice@gmail.com")
	outbox.EnableExternalSMTP(true)
	outbox.EnableMailAliases(true)
	input := composeFixture()
	input.AliasID = &alias.ID
	input.ExternalAccountID = &external
	_, err = outbox.Queue(context.Background(), alice, input)
	assertAppCode(t, err, "mail.invalid_sender")
	input.ExternalAccountID = nil
	_, err = outbox.Queue(context.Background(), bob, input)
	require.Error(t, err)
	_, err = outbox.Queue(context.Background(), alice, input)
	require.NoError(t, err)
}
