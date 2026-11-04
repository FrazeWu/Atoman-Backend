package mail

import (
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func mailTestSetup(t *testing.T) (*gorm.DB, *AccountService, authctx.CurrentUser, authctx.CurrentUser, authctx.CurrentUser) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{})
	require.NoError(t, migrations.RunMailAccountsMigration(db))
	require.NoError(t, migrations.RunMailAccountsMigration(db))
	users := []model.User{
		{Username: "mail-admin", Email: "mail-admin@example.test", Password: "hash", Role: "admin", IsActive: true},
		{Username: "mail-alice", Email: "mail-alice@example.test", Password: "hash", Role: "user", IsActive: true},
		{Username: "mail-bob", Email: "mail-bob@example.test", Password: "hash", Role: "user", IsActive: true},
	}
	for i := range users {
		require.NoError(t, db.Create(&users[i]).Error)
	}
	svc := NewAccountService(db, true)
	svc.now = func() time.Time { return time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC) }
	return db, svc,
		authctx.CurrentUser{ID: users[0].UUID, Username: users[0].Username, Role: "admin"},
		authctx.CurrentUser{ID: users[1].UUID, Username: users[1].Username, Role: "user"},
		authctx.CurrentUser{ID: users[2].UUID, Username: users[2].Username, Role: "user"}
}

func assertAppCode(t *testing.T, err error, code string) {
	t.Helper()
	var appError *apperr.AppError
	require.ErrorAs(t, err, &appError)
	require.Equal(t, code, appError.Code)
}

func TestMailInviteReservationIsUserBoundOneTimeAndImmutable(t *testing.T) {
	db, service, admin, alice, bob := mailTestSetup(t)
	issued, err := service.IssueInvite(admin, IssueInviteInput{TargetUserID: alice.ID, LocalPart: "Fazong.Wu"})
	require.NoError(t, err)
	require.Equal(t, "fazong.wu@atoman.org", issued.Address)
	require.Len(t, issued.Token, 43)
	require.NotContains(t, issued.Token, "=")

	var invite model.MailInvite
	require.NoError(t, db.Where("id = ?", issued.ID).First(&invite).Error)
	require.Equal(t, tokenHash(issued.Token), invite.TokenHash)
	require.NotContains(t, invite.TokenHash, issued.Token)
	require.Equal(t, alice.ID, invite.TargetUserID)

	_, err = service.RedeemInvite(bob, issued.Token)
	assertAppCode(t, err, "mail.invite_owner_required")
	_, err = service.RedeemInvite(alice, "guess")
	assertAppCode(t, err, "mail.invalid_invite")

	info, err := service.RedeemInvite(alice, issued.Token)
	require.NoError(t, err)
	require.Equal(t, "reserved", info.Status)
	require.False(t, info.Available)
	require.Equal(t, issued.Address, info.Address)

	_, err = service.RedeemInvite(alice, issued.Token)
	assertAppCode(t, err, "mail.invite_unavailable")

	var accounts []model.MailAccount
	require.NoError(t, db.Find(&accounts).Error)
	require.Len(t, accounts, 1)
	require.Equal(t, alice.ID, accounts[0].UserID)
	require.Equal(t, "reserved", accounts[0].Status)

	// A user's Atoman username can change without modifying mailbox identity.
	require.NoError(t, db.Model(&model.User{}).Where("uuid = ?", alice.ID).Update("username", "new-alice-name").Error)
	current, err := service.Me(alice)
	require.NoError(t, err)
	require.Equal(t, issued.Address, current.Address)
	require.Equal(t, "reserved", current.Status)

	_, err = service.IssueInvite(admin, IssueInviteInput{TargetUserID: alice.ID, LocalPart: "second"})
	assertAppCode(t, err, "mail.address_unavailable")
	_, err = service.IssueInvite(admin, IssueInviteInput{TargetUserID: bob.ID, LocalPart: "fazong.wu"})
	assertAppCode(t, err, "mail.address_unavailable")
}

func TestMailInvitesEnforceRolesExpirationDuplicateAndReservedNames(t *testing.T) {
	db, service, admin, alice, bob := mailTestSetup(t)
	_, err := service.IssueInvite(alice, IssueInviteInput{TargetUserID: bob.ID, LocalPart: "bob"})
	assertAppCode(t, err, "mail.admin_required")
	for _, invalid := range []string{"a", "admin", "postmaster", "a..b", "x@a", " 中文 ", "abc/def", strings.Repeat("a", 33)} {
		_, err := service.IssueInvite(admin, IssueInviteInput{TargetUserID: bob.ID, LocalPart: invalid})
		require.Error(t, err, "invalid local part %q", invalid)
	}
	_, err = service.IssueInvite(admin, IssueInviteInput{TargetUserID: uuid.New(), LocalPart: "someone"})
	assertAppCode(t, err, "mail.user_not_found")

	issued, err := service.IssueInvite(admin, IssueInviteInput{TargetUserID: bob.ID, LocalPart: "bob-123"})
	require.NoError(t, err)
	_, err = service.IssueInvite(admin, IssueInviteInput{TargetUserID: bob.ID, LocalPart: "bob-123"})
	assertAppCode(t, err, "mail.invite_pending")
	_, err = service.IssueInvite(admin, IssueInviteInput{TargetUserID: alice.ID, LocalPart: "bob-123"})
	assertAppCode(t, err, "mail.invite_pending")

	service.now = func() time.Time { return issued.ExpiresAt.Add(time.Second) }
	_, err = service.RedeemInvite(bob, issued.Token)
	assertAppCode(t, err, "mail.invite_unavailable")
	var count int64
	require.NoError(t, db.Model(&model.MailAccount{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestMailInvitesDefaultOffAndUnknownToken(t *testing.T) {
	_, service, admin, alice, _ := mailTestSetup(t)
	service.invitesEnabled = false
	_, err := service.IssueInvite(admin, IssueInviteInput{TargetUserID: alice.ID, LocalPart: "alice-1"})
	assertAppCode(t, err, "mail.not_ready")
	_, err = service.RedeemInvite(alice, strings.Repeat("x", 43))
	assertAppCode(t, err, "mail.not_ready")
	info, err := service.Me(alice)
	require.NoError(t, err)
	require.Equal(t, "not_invited", info.Status)
	require.False(t, info.Available)
	service.invitesEnabled = true
	_, err = service.RedeemInvite(alice, strings.Repeat("x", 43))
	assertAppCode(t, err, "mail.invalid_invite")
	_, err = service.Me(authctx.CurrentUser{})
	assertAppCode(t, err, "auth.unauthorized")
}

func TestMailInviteDatabaseUniqueConstraints(t *testing.T) {
	db, service, admin, alice, bob := mailTestSetup(t)
	first, err := service.IssueInvite(admin, IssueInviteInput{TargetUserID: alice.ID, LocalPart: "unique-name"})
	require.NoError(t, err)
	_, err = service.RedeemInvite(alice, first.Token)
	require.NoError(t, err)

	// Database constraints defend uniqueness independently of HTTP checks.
	clone := model.MailAccount{ID: uuid.New(), UserID: bob.ID, LocalPart: "unique-name", Address: "unique-name@atoman.org", Status: "reserved"}
	require.Error(t, db.Create(&clone).Error)
	clone = model.MailAccount{ID: uuid.New(), UserID: alice.ID, LocalPart: "new-name", Address: "new-name@atoman.org", Status: "reserved"}
	require.Error(t, db.Create(&clone).Error)
	// Check constraint rejects accidental state transitions to a nonexistent status.
	require.Error(t, db.Model(&model.MailAccount{}).Where("user_id = ?", alice.ID).Update("status", "ready-to-send").Error)

	// No real mail account or content was created in Stalwart.
	var saved model.MailAccount
	require.NoError(t, db.Where("user_id = ?", alice.ID).First(&saved).Error)
	require.Equal(t, "reserved", saved.Status)
}

func TestMailInviteConcurrentRedeemCreatesExactlyOneReservation(t *testing.T) {
	db, service, admin, alice, _ := mailTestSetup(t)
	issued, err := service.IssueInvite(admin, IssueInviteInput{TargetUserID: alice.ID, LocalPart: "singlewinner"})
	require.NoError(t, err)
	var group sync.WaitGroup
	outcomes := make(chan error, 2)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := service.RedeemInvite(alice, issued.Token)
			outcomes <- err
		}()
	}
	group.Wait()
	close(outcomes)
	var success, unavailable int
	for err := range outcomes {
		if err == nil {
			success++
			continue
		}
		var appError *apperr.AppError
		require.ErrorAs(t, err, &appError)
		if appError.Code == "mail.invite_unavailable" || appError.Code == "mail.address_unavailable" {
			unavailable++
		} else {
			t.Fatalf("unexpected concurrent redemption error: %v", err)
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, unavailable)
	var accounts int64
	require.NoError(t, db.Model(&model.MailAccount{}).Where("user_id = ?", alice.ID).Count(&accounts).Error)
	require.EqualValues(t, 1, accounts)
}
