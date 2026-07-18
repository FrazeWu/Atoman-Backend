package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/oauthprovider"
	"atoman/internal/testdb"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type fakeOAuthProvider struct {
	name         string
	authorizeReq oauthprovider.AuthorizationRequest
	callbackReq  oauthprovider.CallbackRequest
	profile      oauthprovider.Profile
	exchangeErr  error
}

func (p *fakeOAuthProvider) Name() string {
	return p.name
}

func (p *fakeOAuthProvider) AuthorizationURL(req oauthprovider.AuthorizationRequest) (string, error) {
	p.authorizeReq = req
	return "https://provider.example/authorize?state=" + url.QueryEscape(req.State), nil
}

func (p *fakeOAuthProvider) Exchange(_ context.Context, req oauthprovider.CallbackRequest) (oauthprovider.Profile, error) {
	p.callbackReq = req
	return p.profile, p.exchangeErr
}

func newOAuthServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.UserSettings{},
		&model.ExternalIdentity{},
		&model.OAuthFlow{},
		&model.Channel{},
		&model.Collection{},
		&model.UserStudioState{},
		&model.StudioModuleSettings{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
		&model.BookmarkFolder{},
		&model.Playlist{},
		&model.PlaylistSong{},
	)
	return db
}

func TestOAuthServiceBeginStoresHashedStateAndPKCE(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	provider := &fakeOAuthProvider{name: model.OAuthProviderGoogle}
	svc := NewOAuthService(db, oauthprovider.NewRegistry(provider))

	result, err := svc.Begin(context.Background(), OAuthBeginInput{
		Provider: model.OAuthProviderGoogle,
		Purpose:  model.OAuthPurposeLogin,
		ReturnTo: "/posts?tab=latest",
	})
	if err != nil {
		t.Fatalf("begin oauth: %v", err)
	}
	if result.AuthorizationURL == "" || provider.authorizeReq.State == "" {
		t.Fatalf("expected authorization url and state, got %#v", result)
	}
	if provider.authorizeReq.CodeChallenge == "" || provider.authorizeReq.Nonce == "" {
		t.Fatalf("expected PKCE and nonce, got %#v", provider.authorizeReq)
	}

	var flow model.OAuthFlow
	if err := db.First(&flow).Error; err != nil {
		t.Fatalf("load flow: %v", err)
	}
	stateSum := sha256.Sum256([]byte(provider.authorizeReq.State))
	if flow.SecretHash != hex.EncodeToString(stateSum[:]) {
		t.Fatalf("state was not stored as a hash: %q", flow.SecretHash)
	}
	if flow.CodeVerifier == "" || flow.NonceHash == "" {
		t.Fatalf("expected verifier and nonce hash, got %#v", flow)
	}
	if flow.ReturnTo != "/posts?tab=latest" || flow.Stage != model.OAuthStageStarted {
		t.Fatalf("unexpected stored flow: %#v", flow)
	}
}

func TestOAuthServiceCallbackLogsInLinkedIdentityOnce(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	user := model.User{Username: "linked-user", Email: "linked@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	identity := model.ExternalIdentity{
		UserID: user.UUID, Provider: model.OAuthProviderGoogle,
		Issuer: "https://accounts.google.com", Subject: "linked-subject",
		Email: user.Email, EmailVerified: true,
	}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatalf("create identity: %v", err)
	}
	provider := &fakeOAuthProvider{
		name: model.OAuthProviderGoogle,
		profile: oauthprovider.Profile{
			Issuer: "https://accounts.google.com", Subject: "linked-subject",
			Email: user.Email, EmailVerified: true, DisplayName: "Linked User",
		},
	}
	svc := NewOAuthService(db, oauthprovider.NewRegistry(provider))
	if _, err := svc.Begin(context.Background(), OAuthBeginInput{Provider: provider.name, ReturnTo: "/forum"}); err != nil {
		t.Fatalf("begin oauth: %v", err)
	}

	result, err := svc.HandleCallback(context.Background(), OAuthCallbackInput{
		Provider: provider.name,
		State:    provider.authorizeReq.State,
		Code:     "authorization-code",
	})
	if err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	if result.Status != OAuthCallbackAuthenticated || result.User == nil || result.User.UUID != user.UUID {
		t.Fatalf("unexpected callback result: %#v", result)
	}
	if result.ReturnTo != "/forum" {
		t.Fatalf("unexpected return path: %q", result.ReturnTo)
	}
	if provider.callbackReq.CodeVerifier == "" || provider.callbackReq.NonceHash == "" {
		t.Fatalf("expected verifier and nonce hash, got %#v", provider.callbackReq)
	}

	var updated model.ExternalIdentity
	if err := db.First(&updated, "uuid = ?", identity.UUID).Error; err != nil {
		t.Fatalf("reload identity: %v", err)
	}
	if updated.LastLoginAt == nil || updated.DisplayName != "Linked User" {
		t.Fatalf("identity login metadata not updated: %#v", updated)
	}

	if _, err := svc.HandleCallback(context.Background(), OAuthCallbackInput{
		Provider: provider.name,
		State:    provider.authorizeReq.State,
		Code:     "authorization-code",
	}); err == nil {
		t.Fatal("expected callback state to be one-time")
	}
}

func TestOAuthServiceCallbackRequiresPasswordWhenVerifiedEmailExists(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	user := model.User{Username: "existing-user", Email: "existing@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	provider := &fakeOAuthProvider{
		name: model.OAuthProviderGitHub,
		profile: oauthprovider.Profile{
			Issuer: "https://github.com", Subject: "github-subject",
			Email: "EXISTING@example.com", EmailVerified: true,
		},
	}
	svc := NewOAuthService(db, oauthprovider.NewRegistry(provider))
	if _, err := svc.Begin(context.Background(), OAuthBeginInput{Provider: provider.name}); err != nil {
		t.Fatalf("begin oauth: %v", err)
	}
	state := provider.authorizeReq.State

	result, err := svc.HandleCallback(context.Background(), OAuthCallbackInput{
		Provider: provider.name, State: state, Code: "code",
	})
	if err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	if result.Status != OAuthCallbackPending || result.Stage != model.OAuthStageConfirmAccount || result.PendingToken == "" {
		t.Fatalf("unexpected pending result: %#v", result)
	}

	var flow model.OAuthFlow
	if err := db.First(&flow).Error; err != nil {
		t.Fatalf("load flow: %v", err)
	}
	if flow.UserID == nil || *flow.UserID != user.UUID || flow.SecretHash != hashOAuthSecret(result.PendingToken) {
		t.Fatalf("unexpected pending flow: %#v", flow)
	}
	if flow.SecretHash == hashOAuthSecret(state) {
		t.Fatal("expected callback state to be rotated")
	}
	if flow.Email != "existing@example.com" || flow.Subject != "github-subject" {
		t.Fatalf("provider profile was not stored: %#v", flow)
	}
}

func TestOAuthServiceCompleteProfileCreatesOAuthOnlyUserAndDefaults(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	provider := &fakeOAuthProvider{
		name: model.OAuthProviderMicrosoft,
		profile: oauthprovider.Profile{
			Issuer: "https://login.microsoftonline.com/tenant/v2.0", Subject: "microsoft-subject",
			Email: "new-user@example.com", EmailVerified: true,
			DisplayName: "New User", AvatarURL: "https://images.example/avatar.png",
		},
	}
	svc := NewOAuthService(db, oauthprovider.NewRegistry(provider))
	if _, err := svc.Begin(context.Background(), OAuthBeginInput{Provider: provider.name, ReturnTo: "/music"}); err != nil {
		t.Fatalf("begin oauth: %v", err)
	}
	callback, err := svc.HandleCallback(context.Background(), OAuthCallbackInput{
		Provider: provider.name, State: provider.authorizeReq.State, Code: "code",
	})
	if err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	if callback.Stage != model.OAuthStageCompleteProfile || callback.PendingToken == "" {
		t.Fatalf("expected profile completion, got %#v", callback)
	}

	completed, err := svc.CompleteProfile(context.Background(), OAuthCompleteProfileInput{
		PendingToken: callback.PendingToken,
		Username:     "new-user",
	})
	if err != nil {
		t.Fatalf("complete profile: %v", err)
	}
	if completed.User.Username != "new-user" || completed.User.Email != "new-user@example.com" || completed.User.Password != "" {
		t.Fatalf("unexpected oauth user: %#v", completed.User)
	}
	if completed.User.DisplayName != "New User" || completed.User.AvatarURL != "https://images.example/avatar.png" {
		t.Fatalf("provider profile not copied: %#v", completed.User)
	}
	if completed.ReturnTo != "/music" {
		t.Fatalf("unexpected return path: %q", completed.ReturnTo)
	}

	var identity model.ExternalIdentity
	if err := db.First(&identity, "user_id = ?", completed.User.UUID).Error; err != nil {
		t.Fatalf("load identity: %v", err)
	}
	if identity.Provider != provider.name || identity.Subject != "microsoft-subject" {
		t.Fatalf("unexpected identity: %#v", identity)
	}
	var settings model.UserSettings
	if err := db.First(&settings, "user_id = ?", completed.User.UUID).Error; err != nil {
		t.Fatalf("load user settings: %v", err)
	}
	var channel model.Channel
	if err := db.First(&channel, "user_id = ?", completed.User.UUID).Error; err != nil {
		t.Fatalf("load default channel: %v", err)
	}

	if _, err := svc.CompleteProfile(context.Background(), OAuthCompleteProfileInput{
		PendingToken: callback.PendingToken, Username: "second-user",
	}); err == nil {
		t.Fatal("expected pending flow to be one-time")
	}
}

func TestOAuthServicePendingInfoRejectsUnavailableProvider(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	token := "removed-provider-pending-token"
	flow := model.OAuthFlow{
		SecretHash: hashOAuthSecret(token), Provider: "removed-provider",
		Purpose: model.OAuthPurposeLogin, Stage: model.OAuthStageCompleteProfile,
		Email: "pending@example.com", EmailVerified: true,
		Issuer: "https://removed.example", Subject: "removed-subject",
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	if err := db.Create(&flow).Error; err != nil {
		t.Fatalf("create pending flow: %v", err)
	}
	svc := NewOAuthService(db, oauthprovider.NewRegistry())

	if _, err := svc.PendingInfo(context.Background(), token); err == nil {
		t.Fatal("expected unavailable provider pending info to fail")
	}
}

func TestOAuthServiceCompleteProfileRejectsUnavailableProvider(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	token := "removed-provider-profile-token"
	flow := model.OAuthFlow{
		SecretHash: hashOAuthSecret(token), Provider: "removed-provider",
		Purpose: model.OAuthPurposeLogin, Stage: model.OAuthStageCompleteProfile,
		Email: "removed@example.com", EmailVerified: true,
		Issuer: "https://removed.example", Subject: "removed-subject",
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	if err := db.Create(&flow).Error; err != nil {
		t.Fatalf("create pending flow: %v", err)
	}
	svc := NewOAuthService(db, oauthprovider.NewRegistry())

	if _, err := svc.CompleteProfile(context.Background(), OAuthCompleteProfileInput{
		PendingToken: token, Username: "removed-user",
	}); err == nil {
		t.Fatal("expected unavailable provider profile completion to fail")
	}
	var count int64
	if err := db.Model(&model.User{}).Count(&count).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no user for unavailable provider, got %d", count)
	}
}

func TestOAuthServiceConfirmAccountRejectsUnavailableProvider(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := model.User{
		Username: "existing-removed", Email: "existing-removed@example.com",
		Password: string(hash), Role: "user", IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	token := "removed-provider-confirm-token"
	flow := model.OAuthFlow{
		SecretHash: hashOAuthSecret(token), Provider: "removed-provider",
		Purpose: model.OAuthPurposeLogin, Stage: model.OAuthStageConfirmAccount,
		UserID: &user.UUID, Email: user.Email, EmailVerified: true,
		Issuer: "https://removed.example", Subject: "removed-subject",
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	if err := db.Create(&flow).Error; err != nil {
		t.Fatalf("create pending flow: %v", err)
	}
	svc := NewOAuthService(db, oauthprovider.NewRegistry())

	if _, err := svc.ConfirmAccount(context.Background(), OAuthConfirmAccountInput{
		PendingToken: token, Password: "correct-password",
	}); err == nil {
		t.Fatal("expected unavailable provider account confirmation to fail")
	}
	var count int64
	if err := db.Model(&model.ExternalIdentity{}).Count(&count).Error; err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no identity for unavailable provider, got %d", count)
	}
}

func TestOAuthServiceConfirmAccountRequiresCorrectPasswordBeforeBinding(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := model.User{
		Username: "existing", Email: "existing@example.com", Password: string(hash), Role: "user", IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	provider := &fakeOAuthProvider{
		name: model.OAuthProviderMicrosoft,
		profile: oauthprovider.Profile{
			Issuer: "https://login.microsoftonline.com/common/v2.0", Subject: "microsoft-subject",
			Email: user.Email, EmailVerified: true,
		},
	}
	svc := NewOAuthService(db, oauthprovider.NewRegistry(provider))
	if _, err := svc.Begin(context.Background(), OAuthBeginInput{Provider: provider.name}); err != nil {
		t.Fatalf("begin oauth: %v", err)
	}
	callback, err := svc.HandleCallback(context.Background(), OAuthCallbackInput{
		Provider: provider.name, State: provider.authorizeReq.State, Code: "code",
	})
	if err != nil {
		t.Fatalf("handle callback: %v", err)
	}

	if _, err := svc.ConfirmAccount(context.Background(), OAuthConfirmAccountInput{
		PendingToken: callback.PendingToken, Password: "wrong-password",
	}); err == nil {
		t.Fatal("expected wrong password to fail")
	}
	var count int64
	if err := db.Model(&model.ExternalIdentity{}).Count(&count).Error; err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no identity after wrong password, got %d", count)
	}

	confirmed, err := svc.ConfirmAccount(context.Background(), OAuthConfirmAccountInput{
		PendingToken: callback.PendingToken, Password: "correct-password",
	})
	if err != nil {
		t.Fatalf("confirm account: %v", err)
	}
	if confirmed.User.UUID != user.UUID {
		t.Fatalf("bound wrong user: %#v", confirmed.User)
	}
	if err := db.Model(&model.ExternalIdentity{}).Where("user_id = ? AND provider = ?", user.UUID, provider.name).Count(&count).Error; err != nil {
		t.Fatalf("count bound identities: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one bound identity, got %d", count)
	}
	if _, err := svc.ConfirmAccount(context.Background(), OAuthConfirmAccountInput{
		PendingToken: callback.PendingToken, Password: "correct-password",
	}); err == nil {
		t.Fatal("expected confirmed flow to be one-time")
	}
}

func TestOAuthServiceLinkPurposeBindsIdentityToCurrentUser(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	user := model.User{Username: "current-user", Email: "current@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	provider := &fakeOAuthProvider{
		name: model.OAuthProviderGoogle,
		profile: oauthprovider.Profile{
			Issuer: "https://accounts.google.com", Subject: "new-google-subject",
			Email: "provider-email@example.com", EmailVerified: true,
		},
	}
	svc := NewOAuthService(db, oauthprovider.NewRegistry(provider))
	if _, err := svc.Begin(context.Background(), OAuthBeginInput{
		Provider: provider.name, Purpose: model.OAuthPurposeLink, UserID: &user.UUID, ReturnTo: "/users/current-user/settings",
	}); err != nil {
		t.Fatalf("begin link: %v", err)
	}

	result, err := svc.HandleCallback(context.Background(), OAuthCallbackInput{
		Provider: provider.name, State: provider.authorizeReq.State, Code: "code",
	})
	if err != nil {
		t.Fatalf("handle link callback: %v", err)
	}
	if result.Status != OAuthCallbackAuthenticated || result.User == nil || result.User.UUID != user.UUID {
		t.Fatalf("unexpected link result: %#v", result)
	}

	var identity model.ExternalIdentity
	if err := db.First(&identity, "user_id = ? AND provider = ?", user.UUID, provider.name).Error; err != nil {
		t.Fatalf("load linked identity: %v", err)
	}
	if identity.Subject != "new-google-subject" {
		t.Fatalf("unexpected linked identity: %#v", identity)
	}
}

func TestOAuthServiceUnlinkKeepsAtLeastOneLoginMethod(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	user := model.User{Username: "oauth-only", Email: "oauth-only@example.com", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	identities := []model.ExternalIdentity{
		{UserID: user.UUID, Provider: model.OAuthProviderGoogle, Issuer: "google", Subject: "google-sub", Email: user.Email, EmailVerified: true},
		{UserID: user.UUID, Provider: model.OAuthProviderGitHub, Issuer: "github", Subject: "github-sub", Email: user.Email, EmailVerified: true},
	}
	if err := db.Create(&identities).Error; err != nil {
		t.Fatalf("create identities: %v", err)
	}
	svc := NewOAuthService(db, oauthprovider.NewRegistry())

	listed, err := svc.ListIdentities(context.Background(), user.UUID)
	if err != nil {
		t.Fatalf("list identities: %v", err)
	}
	if len(listed) != 2 || listed[0].Provider != model.OAuthProviderGitHub || listed[1].Provider != model.OAuthProviderGoogle {
		t.Fatalf("unexpected identity list: %#v", listed)
	}

	if err := svc.Unlink(context.Background(), user.UUID, model.OAuthProviderGoogle); err != nil {
		t.Fatalf("unlink with fallback identity: %v", err)
	}
	if err := svc.Unlink(context.Background(), user.UUID, model.OAuthProviderGitHub); err == nil {
		t.Fatal("expected last login method unlink to fail")
	}

	if err := db.Model(&model.User{}).Where("uuid = ?", user.UUID).Update("password", "hash").Error; err != nil {
		t.Fatalf("set password: %v", err)
	}
	if err := svc.Unlink(context.Background(), user.UUID, model.OAuthProviderGitHub); err != nil {
		t.Fatalf("unlink with password fallback: %v", err)
	}
}

func TestOAuthServiceUnlinkLocksUserBeforeCountingLoginMethods(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	user := model.User{Username: "locked-user", Email: "locked@example.com", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	identity := model.ExternalIdentity{
		UserID: user.UUID, Provider: model.OAuthProviderGoogle,
		Issuer: "google", Subject: "locked-google", Email: user.Email, EmailVerified: true,
	}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatalf("create identity: %v", err)
	}

	userQueryLocked := false
	callbackName := "test:detect_oauth_user_lock"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if strings.EqualFold(tx.Statement.Table, "users") {
			_, userQueryLocked = tx.Statement.Clauses["FOR"]
		}
	}); err != nil {
		t.Fatalf("register query callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	svc := NewOAuthService(db, oauthprovider.NewRegistry())
	if err := svc.Unlink(context.Background(), user.UUID, model.OAuthProviderGoogle); err == nil {
		t.Fatal("expected last login method unlink to fail")
	}
	if !userQueryLocked {
		t.Fatal("expected unlink to lock the user row before counting login methods")
	}
}
