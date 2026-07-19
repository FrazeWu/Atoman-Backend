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
	"atoman/internal/platform/apperr"
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

func TestOAuthServiceCallbackRequiresPasswordSetupForLinkedOAuthOnlyUser(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	user := model.User{Username: "oauth-only", Email: "oauth-only@example.com", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	identity := model.ExternalIdentity{
		UserID: user.UUID, Provider: model.OAuthProviderGitHub,
		Issuer: "https://github.com", Subject: "linked-subject",
		Email: user.Email, EmailVerified: true,
	}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatalf("create identity: %v", err)
	}
	provider := &fakeOAuthProvider{
		name: model.OAuthProviderGitHub,
		profile: oauthprovider.Profile{
			Issuer: "https://github.com", Subject: "linked-subject",
			Email: user.Email, EmailVerified: true,
		},
	}
	svc := NewOAuthService(db, oauthprovider.NewRegistry(provider))
	if _, err := svc.Begin(context.Background(), OAuthBeginInput{Provider: provider.name, ReturnTo: "/forum"}); err != nil {
		t.Fatalf("begin oauth: %v", err)
	}

	result, err := svc.HandleCallback(context.Background(), OAuthCallbackInput{
		Provider: provider.name, State: provider.authorizeReq.State, Code: "code",
	})
	if err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	if result.Status != OAuthCallbackPending || result.Stage != model.OAuthStageSetPassword || result.PendingToken == "" {
		t.Fatalf("expected password setup, got %#v", result)
	}
	if result.User != nil || result.ReturnTo != "/forum" {
		t.Fatalf("unexpected pending callback result: %#v", result)
	}

	var flow model.OAuthFlow
	if err := db.First(&flow).Error; err != nil {
		t.Fatalf("load flow: %v", err)
	}
	if flow.Stage != model.OAuthStageSetPassword || flow.UserID == nil || *flow.UserID != user.UUID {
		t.Fatalf("unexpected password setup flow: %#v", flow)
	}
	if flow.SecretHash != hashOAuthSecret(result.PendingToken) || flow.ConsumedAt != nil {
		t.Fatalf("expected active rotated flow: %#v", flow)
	}
}

func TestOAuthServiceSetPasswordCompletesOAuthOnlyUserLogin(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	user := model.User{Username: "oauth-migration", Email: "migration@example.com", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.ExternalIdentity{
		UserID: user.UUID, Provider: model.OAuthProviderGoogle,
		Issuer: "https://accounts.google.com", Subject: "migration-subject",
		Email: user.Email, EmailVerified: true,
	}).Error; err != nil {
		t.Fatalf("create identity: %v", err)
	}
	provider := &fakeOAuthProvider{
		name: model.OAuthProviderGoogle,
		profile: oauthprovider.Profile{
			Issuer: "https://accounts.google.com", Subject: "migration-subject",
			Email: user.Email, EmailVerified: true,
		},
	}
	svc := NewOAuthService(db, oauthprovider.NewRegistry(provider))
	if _, err := svc.Begin(context.Background(), OAuthBeginInput{Provider: provider.name, ReturnTo: "/posts"}); err != nil {
		t.Fatalf("begin oauth: %v", err)
	}
	callback, err := svc.HandleCallback(context.Background(), OAuthCallbackInput{
		Provider: provider.name, State: provider.authorizeReq.State, Code: "code",
	})
	if err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	info, err := svc.PendingInfo(context.Background(), callback.PendingToken)
	if err != nil {
		t.Fatalf("get password setup info: %v", err)
	}
	if info.Stage != model.OAuthStageSetPassword || info.HasPassword {
		t.Fatalf("unexpected password setup info: %#v", info)
	}

	completed, err := svc.SetPassword(context.Background(), OAuthSetPasswordInput{
		PendingToken: callback.PendingToken,
		Password:     "secret123", PasswordConfirm: "secret123",
	})
	if err != nil {
		t.Fatalf("set password: %v", err)
	}
	if completed.User.UUID != user.UUID || completed.ReturnTo != "/posts" {
		t.Fatalf("unexpected completion: %#v", completed)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(completed.User.Password), []byte("secret123")); err != nil {
		t.Fatalf("stored password is not a matching bcrypt hash: %v", err)
	}
	var flow model.OAuthFlow
	if err := db.First(&flow).Error; err != nil {
		t.Fatalf("load flow: %v", err)
	}
	if flow.ConsumedAt == nil {
		t.Fatal("expected password setup flow to be consumed")
	}
	if _, err := svc.SetPassword(context.Background(), OAuthSetPasswordInput{
		PendingToken: callback.PendingToken,
		Password:     "secret123", PasswordConfirm: "secret123",
	}); err == nil {
		t.Fatal("expected password setup flow to be one-time")
	}
}

func TestOAuthServiceSetPasswordRejectsInvalidPassword(t *testing.T) {
	tests := []struct {
		name            string
		password        string
		passwordConfirm string
		wantCode        string
	}{
		{name: "too short", password: "12345", passwordConfirm: "12345", wantCode: "oauth.password_too_short"},
		{name: "too long", password: strings.Repeat("a", 73), passwordConfirm: strings.Repeat("a", 73), wantCode: "oauth.password_too_long"},
		{name: "confirmation mismatch", password: "secret123", passwordConfirm: "different", wantCode: "oauth.password_mismatch"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newOAuthServiceTestDB(t)
			user := model.User{Username: "oauth-migration", Email: "migration@example.com", Role: "user", IsActive: true}
			if err := db.Create(&user).Error; err != nil {
				t.Fatalf("create user: %v", err)
			}
			token := "invalid-set-password-token"
			flow := model.OAuthFlow{
				SecretHash: hashOAuthSecret(token), Provider: model.OAuthProviderGitHub,
				Purpose: model.OAuthPurposeLogin, Stage: model.OAuthStageSetPassword,
				UserID: &user.UUID, Email: user.Email, EmailVerified: true,
				Issuer: "https://github.com", Subject: "migration-subject",
				ExpiresAt: time.Now().UTC().Add(time.Minute),
			}
			if err := db.Create(&flow).Error; err != nil {
				t.Fatalf("create pending flow: %v", err)
			}
			svc := NewOAuthService(db, oauthprovider.NewRegistry(&fakeOAuthProvider{name: model.OAuthProviderGitHub}))

			_, err := svc.SetPassword(context.Background(), OAuthSetPasswordInput{
				PendingToken: token, Password: tt.password, PasswordConfirm: tt.passwordConfirm,
			})
			if err == nil {
				t.Fatal("expected invalid password to fail")
			}
			if code := apperr.FromError(err).Code; code != tt.wantCode {
				t.Fatalf("unexpected error code: got %q want %q", code, tt.wantCode)
			}
			var updated model.User
			if err := db.First(&updated, "uuid = ?", user.UUID).Error; err != nil {
				t.Fatalf("reload user: %v", err)
			}
			if updated.Password != "" {
				t.Fatal("expected password to remain unset")
			}
		})
	}
}

func TestOAuthServiceSetPasswordDoesNotOverwriteExistingPassword(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	originalHash, err := bcrypt.GenerateFromPassword([]byte("original-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash original password: %v", err)
	}
	user := model.User{
		Username: "already-migrated", Email: "migrated@example.com",
		Password: string(originalHash), Role: "user", IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.ExternalIdentity{
		UserID: user.UUID, Provider: model.OAuthProviderMicrosoft,
		Issuer: "https://login.microsoftonline.com/common/v2.0", Subject: "migration-subject",
		Email: user.Email, EmailVerified: true,
	}).Error; err != nil {
		t.Fatalf("create identity: %v", err)
	}
	token := "stale-set-password-token"
	flow := model.OAuthFlow{
		SecretHash: hashOAuthSecret(token), Provider: model.OAuthProviderMicrosoft,
		Purpose: model.OAuthPurposeLogin, Stage: model.OAuthStageSetPassword,
		UserID: &user.UUID, Email: user.Email, EmailVerified: true,
		Issuer: "https://login.microsoftonline.com/common/v2.0", Subject: "migration-subject",
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	if err := db.Create(&flow).Error; err != nil {
		t.Fatalf("create pending flow: %v", err)
	}
	svc := NewOAuthService(db, oauthprovider.NewRegistry(&fakeOAuthProvider{name: model.OAuthProviderMicrosoft}))

	_, err = svc.SetPassword(context.Background(), OAuthSetPasswordInput{
		PendingToken: token, Password: "replacement-password", PasswordConfirm: "replacement-password",
	})
	if err == nil {
		t.Fatal("expected existing password to reject setup")
	}
	if code := apperr.FromError(err).Code; code != "oauth.password_already_set" {
		t.Fatalf("unexpected error code: %q", code)
	}
	var updated model.User
	if err := db.First(&updated, "uuid = ?", user.UUID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte("original-password")); err != nil {
		t.Fatalf("original password was overwritten: %v", err)
	}
}

func TestOAuthServiceSetPasswordRejectsFlowForUnlinkedIdentity(t *testing.T) {
	db := newOAuthServiceTestDB(t)
	user := model.User{Username: "unlink-before-password", Email: "unlink@example.com", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	identities := []model.ExternalIdentity{
		{
			UserID: user.UUID, Provider: model.OAuthProviderGoogle,
			Issuer: "https://accounts.google.com", Subject: "google-subject",
			Email: user.Email, EmailVerified: true,
		},
		{
			UserID: user.UUID, Provider: model.OAuthProviderGitHub,
			Issuer: "https://github.com", Subject: "github-subject",
			Email: user.Email, EmailVerified: true,
		},
	}
	if err := db.Create(&identities).Error; err != nil {
		t.Fatalf("create identities: %v", err)
	}
	provider := &fakeOAuthProvider{
		name: model.OAuthProviderGoogle,
		profile: oauthprovider.Profile{
			Issuer: "https://accounts.google.com", Subject: "google-subject",
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
	if err := svc.Unlink(context.Background(), user.UUID, model.OAuthProviderGoogle); err != nil {
		t.Fatalf("unlink identity: %v", err)
	}

	_, err = svc.SetPassword(context.Background(), OAuthSetPasswordInput{
		PendingToken: callback.PendingToken,
		Password:     "secret123", PasswordConfirm: "secret123",
	})
	if err == nil {
		t.Fatal("expected flow for unlinked identity to fail")
	}
	if code := apperr.FromError(err).Code; code != "oauth.identity_unlinked" {
		t.Fatalf("unexpected error code: %q", code)
	}
	var updated model.User
	if err := db.First(&updated, "uuid = ?", user.UUID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if updated.Password != "" {
		t.Fatal("unlinked identity set a local password")
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

func TestOAuthServicePendingInfoReportsWhetherExistingAccountHasPassword(t *testing.T) {
	tests := []struct {
		name        string
		password    string
		hasPassword bool
	}{
		{name: "local password exists", password: "stored-password-hash", hasPassword: true},
		{name: "oauth only account", password: "", hasPassword: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newOAuthServiceTestDB(t)
			user := model.User{
				Username: "same-email-user", Email: "same-email@example.com",
				Password: tt.password, Role: "user", IsActive: true,
			}
			if err := db.Create(&user).Error; err != nil {
				t.Fatalf("create user: %v", err)
			}
			provider := &fakeOAuthProvider{
				name: model.OAuthProviderGitHub,
				profile: oauthprovider.Profile{
					Issuer: "https://github.com", Subject: "unlinked-subject",
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
			if callback.Stage != model.OAuthStageConfirmAccount {
				t.Fatalf("expected account confirmation, got %#v", callback)
			}

			info, err := svc.PendingInfo(context.Background(), callback.PendingToken)
			if err != nil {
				t.Fatalf("get pending info: %v", err)
			}
			if info.HasPassword != tt.hasPassword {
				t.Fatalf("unexpected has_password: got %t want %t", info.HasPassword, tt.hasPassword)
			}
			var count int64
			if err := db.Model(&model.ExternalIdentity{}).Count(&count).Error; err != nil {
				t.Fatalf("count identities: %v", err)
			}
			if count != 0 {
				t.Fatalf("same email was automatically linked to %d identities", count)
			}
		})
	}
}

func TestOAuthServiceCompleteProfileCreatesUserWithPasswordAndDefaults(t *testing.T) {
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
		PendingToken:    callback.PendingToken,
		Username:        "new-user",
		Password:        "secret123",
		PasswordConfirm: "secret123",
	})
	if err != nil {
		t.Fatalf("complete profile: %v", err)
	}
	if completed.User.Username != "new-user" || completed.User.Email != "new-user@example.com" || completed.User.Password == "" {
		t.Fatalf("unexpected oauth user: %#v", completed.User)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(completed.User.Password), []byte("secret123")); err != nil {
		t.Fatalf("stored password is not a matching bcrypt hash: %v", err)
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

func TestOAuthServiceCompleteProfileRejectsInvalidPassword(t *testing.T) {
	tests := []struct {
		name            string
		password        string
		passwordConfirm string
		wantCode        string
	}{
		{name: "too short", password: "12345", passwordConfirm: "12345", wantCode: "oauth.password_too_short"},
		{name: "too long", password: strings.Repeat("a", 73), passwordConfirm: strings.Repeat("a", 73), wantCode: "oauth.password_too_long"},
		{name: "confirmation mismatch", password: "secret123", passwordConfirm: "different", wantCode: "oauth.password_mismatch"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newOAuthServiceTestDB(t)
			token := "invalid-password-token"
			flow := model.OAuthFlow{
				SecretHash: hashOAuthSecret(token), Provider: model.OAuthProviderGoogle,
				Purpose: model.OAuthPurposeLogin, Stage: model.OAuthStageCompleteProfile,
				Email: "new-user@example.com", EmailVerified: true,
				Issuer: "https://accounts.google.com", Subject: "google-subject",
				ExpiresAt: time.Now().UTC().Add(time.Minute),
			}
			if err := db.Create(&flow).Error; err != nil {
				t.Fatalf("create pending flow: %v", err)
			}
			svc := NewOAuthService(db, oauthprovider.NewRegistry(&fakeOAuthProvider{name: model.OAuthProviderGoogle}))

			_, err := svc.CompleteProfile(context.Background(), OAuthCompleteProfileInput{
				PendingToken: token, Username: "new-user",
				Password: tt.password, PasswordConfirm: tt.passwordConfirm,
			})
			if err == nil {
				t.Fatal("expected invalid password to fail")
			}
			if code := apperr.FromError(err).Code; code != tt.wantCode {
				t.Fatalf("unexpected error code: got %q want %q", code, tt.wantCode)
			}
			var count int64
			if err := db.Model(&model.User{}).Count(&count).Error; err != nil {
				t.Fatalf("count users: %v", err)
			}
			if count != 0 {
				t.Fatalf("expected no user, got %d", count)
			}
		})
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
