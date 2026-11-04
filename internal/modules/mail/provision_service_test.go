package mail

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fakeMailProvisioner struct {
	created     int
	keys        int
	revocations int
	failCreate  bool
	failKey     bool
}

func (f *fakeMailProvisioner) FindDomainID(_ context.Context, _ StalwartCredential, domain string) (string, error) {
	if domain != MailDomain {
		return "", errors.New("unexpected mail domain")
	}
	return "domain-123", nil
}

func (f *fakeMailProvisioner) CreateUser(_ context.Context, _ StalwartCredential, _, _ string, _ uuid.UUID, _ uint64) (StalwartCreatedUser, error) {
	f.created++
	if f.failCreate {
		return StalwartCreatedUser{}, errors.New("network timeout or unknown remote status")
	}
	return StalwartCreatedUser{AccountID: "stalwart-user-123"}, nil
}

func (f *fakeMailProvisioner) CreateUserAPIKey(_ context.Context, _ StalwartCredential, _ string) (string, string, error) {
	f.keys++
	if f.failKey {
		return "", "", errors.New("remote api key failed")
	}
	return "api-key-123", "one-time-staging-secret-value", nil
}

func (f *fakeMailProvisioner) RevokeUserAPIKey(_ context.Context, _ StalwartCredential, _, _ string) error {
	f.revocations++
	return nil
}

func mailProvisionTestSetup(t *testing.T) (*gorm.DB, *MailCredentialVault, authctx.CurrentUser, authctx.CurrentUser, authctx.CurrentUser) {
	t.Helper()
	db, _, admin, alice, bob := mailTestSetup(t)
	account := model.MailAccount{ID: uuid.New(), UserID: alice.ID, LocalPart: "stage-alice", Address: "stage-alice@atoman.org", Status: "reserved"}
	require.NoError(t, db.Create(&account).Error)
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	vault, err := NewMailCredentialVault(base64.StdEncoding.EncodeToString(key))
	require.NoError(t, err)
	return db, vault, admin, alice, bob
}

func TestMailProvisionerRequiresAllOperationalGates(t *testing.T) {
	db, vault, admin, alice, bob := mailProvisionTestSetup(t)
	for _, gates := range []MailActivationGates{
		{},
		{ProvisioningAllowed: true},
		{ProvisioningAllowed: true, R2ReadWriteVerified: true},
		{ProvisioningAllowed: true, TrueNASRestoreVerified: true},
	} {
		provider := &fakeMailProvisioner{}
		service := NewMailProvisionService(db, provider, vault, StalwartBasic("admin", "fixture-password"), gates)
		_, err := service.ProvisionReserved(context.Background(), admin, alice.ID)
		assertAppCode(t, err, "mail.provisioning_not_ready")
		require.Equal(t, 0, provider.created)
	}
	provider := &fakeMailProvisioner{}
	service := NewMailProvisionService(db, provider, vault, StalwartBasic("admin", "fixture-password"), MailActivationGates{true, true, true})
	_, err := service.ProvisionReserved(context.Background(), bob, alice.ID)
	assertAppCode(t, err, "mail.admin_required")
	require.Equal(t, 0, provider.created)
	var account model.MailAccount
	require.NoError(t, db.Where("user_id = ?", alice.ID).First(&account).Error)
	require.Equal(t, "reserved", account.Status)
}

func TestMailProvisionerStoresOnlyEncryptedKeyThenActivates(t *testing.T) {
	db, vault, admin, alice, _ := mailProvisionTestSetup(t)
	provider := &fakeMailProvisioner{}
	service := NewMailProvisionService(db, provider, vault, StalwartBasic("admin", "fixture-password"), MailActivationGates{true, true, true})
	info, err := service.ProvisionReserved(context.Background(), admin, alice.ID)
	require.NoError(t, err)
	require.True(t, info.Available)
	require.Equal(t, "active", info.Status)
	require.Equal(t, "stage-alice@atoman.org", info.Address)
	require.Equal(t, 1, provider.created)
	require.Equal(t, 1, provider.keys)

	var account model.MailAccount
	require.NoError(t, db.Where("user_id = ?", alice.ID).First(&account).Error)
	require.Equal(t, "active", account.Status)
	require.NotNil(t, account.StalwartAccountID)
	require.Equal(t, "stalwart-user-123", *account.StalwartAccountID)
	var credentials model.MailAccessCredential
	require.NoError(t, db.Where("user_id = ?", alice.ID).First(&credentials).Error)
	require.EqualValues(t, 1, credentials.KeyVersion)
	require.NotContains(t, string(credentials.Ciphertext), "one-time-staging-secret-value")
	decrypted, err := vault.Open(alice.ID, "stalwart-user-123", credentials.Ciphertext, credentials.Nonce)
	require.NoError(t, err)
	require.Equal(t, "one-time-staging-secret-value", decrypted)

	_, err = service.ProvisionReserved(context.Background(), admin, alice.ID)
	assertAppCode(t, err, "mail.not_reservable")
	require.Equal(t, 1, provider.created)
	accountService := NewAccountService(db, false)
	me, err := accountService.Me(alice)
	require.NoError(t, err)
	require.True(t, me.Available)
}

func TestMailProvisionerNeverBlindlyRetriesUncertainRemoteCreation(t *testing.T) {
	for _, failure := range []string{"create", "apikey"} {
		t.Run(failure, func(t *testing.T) {
			db, vault, admin, alice, _ := mailProvisionTestSetup(t)
			provider := &fakeMailProvisioner{failCreate: failure == "create", failKey: failure == "apikey"}
			service := NewMailProvisionService(db, provider, vault, StalwartBasic("admin", "fixture-password"), MailActivationGates{true, true, true})
			_, err := service.ProvisionReserved(context.Background(), admin, alice.ID)
			assertAppCode(t, err, "mail.provisioning_reconcile")
			var account model.MailAccount
			require.NoError(t, db.Where("user_id = ?", alice.ID).First(&account).Error)
			require.Equal(t, "provisioning", account.Status)
			var count int64
			require.NoError(t, db.Model(&model.MailAccessCredential{}).Count(&count).Error)
			require.Zero(t, count)
			_, err = service.ProvisionReserved(context.Background(), admin, alice.ID)
			assertAppCode(t, err, "mail.not_reservable")
			require.Equal(t, 1, provider.created)
		})
	}
}
