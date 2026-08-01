package authsession

import (
	"errors"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func newTestService(t *testing.T) (*Service, model.User) {
	t.Helper()
	db := testdb.Open(t)
	if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}); err != nil {
		t.Fatalf("migrate auth session schema: %v", err)
	}
	user := model.User{Username: "session-user", Email: "session@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return New(db), user
}

func TestServiceStoresOnlyTokenHashes(t *testing.T) {
	sessions, user := newTestService(t)
	sessions.now = func() time.Time { return time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC) }
	credentials, err := sessions.Create(user.UUID, KindWeb)
	if err != nil {
		t.Fatalf("create web session: %v", err)
	}
	if credentials.Token == "" || credentials.CSRFToken == "" {
		t.Fatalf("expected web and csrf tokens, got %#v", credentials)
	}
	var stored model.AuthSession
	if err := sessions.db.First(&stored).Error; err != nil {
		t.Fatalf("load stored session: %v", err)
	}
	if stored.TokenHash == credentials.Token || stored.CSRFHash == credentials.CSRFToken {
		t.Fatal("raw session credentials must not be stored")
	}
	resolved, err := sessions.Authenticate(credentials.Token, KindWeb)
	if err != nil || resolved.User.UUID != user.UUID || resolved.Session.ID != stored.ID {
		t.Fatalf("unexpected resolved session %#v, err %v", resolved, err)
	}
	if !sessions.VerifyCSRF(resolved.Session, credentials.CSRFToken) {
		t.Fatal("expected csrf token to verify")
	}
}

func TestServiceRejectsSessionCreatedForPreviousAuthVersion(t *testing.T) {
	sessions, user := newTestService(t)

	if err := sessions.db.Model(&user).Update("auth_version", 1).Error; err != nil {
		t.Fatalf("advance auth version: %v", err)
	}
	credentials, err := sessions.CreateAtVersion(user.UUID, KindAPI, 0)
	if err != nil {
		t.Fatalf("create stale session: %v", err)
	}

	if _, err := sessions.Authenticate(credentials.Token, KindAPI); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected stale session to be invalid, got %v", err)
	}
}

func TestServiceCreateUsesCurrentAuthVersion(t *testing.T) {
	sessions, user := newTestService(t)
	if err := sessions.db.Model(&user).Update("auth_version", 3).Error; err != nil {
		t.Fatalf("advance auth version: %v", err)
	}

	credentials, err := sessions.Create(user.UUID, KindAPI)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	var stored model.AuthSession
	if err := sessions.db.First(&stored, "token_hash = ?", Hash(credentials.Token)).Error; err != nil {
		t.Fatalf("load session: %v", err)
	}
	if stored.AuthVersion != 3 {
		t.Fatalf("expected auth version 3, got %d", stored.AuthVersion)
	}
}

func TestServiceRevokeUserExceptKeepsCurrentSessionOnNewVersion(t *testing.T) {
	sessions, user := newTestService(t)
	current, err := sessions.Create(user.UUID, KindAPI)
	if err != nil {
		t.Fatalf("create current session: %v", err)
	}
	other, err := sessions.Create(user.UUID, KindWeb)
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}
	var currentSession model.AuthSession
	if err := sessions.db.First(&currentSession, "token_hash = ?", Hash(current.Token)).Error; err != nil {
		t.Fatalf("load current session: %v", err)
	}
	if err := sessions.db.Model(&user).Update("auth_version", 1).Error; err != nil {
		t.Fatalf("advance auth version: %v", err)
	}
	if err := sessions.RevokeUserExcept(user.UUID, currentSession.ID); err != nil {
		t.Fatalf("revoke other sessions: %v", err)
	}

	if _, err := sessions.Authenticate(current.Token, KindAPI); err != nil {
		t.Fatalf("expected current session to remain valid, got %v", err)
	}
	if _, err := sessions.Authenticate(other.Token, KindWeb); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected other session to be invalid, got %v", err)
	}
}

func TestServiceRevokesSessionImmediately(t *testing.T) {
	sessions, user := newTestService(t)
	credentials, err := sessions.Create(user.UUID, KindAPI)
	if err != nil {
		t.Fatalf("create api session: %v", err)
	}
	if credentials.CSRFToken != "" {
		t.Fatal("api sessions must not issue csrf tokens")
	}
	if err := sessions.Revoke(credentials.Token); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if _, err := sessions.Authenticate(credentials.Token, KindAPI); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected revoked session to be invalid, got %v", err)
	}
}

func TestServiceListsSessionsWithDeviceMetadata(t *testing.T) {
	sessions, user := newTestService(t)
	credentials, err := sessions.Create(user.UUID, KindWeb, Metadata{
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15",
		IPAddress: "203.0.113.19",
		IPPrefix:  "203.0.113.0/24",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	items, err := sessions.List(user.UUID, credentials.Token)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one session, got %d", len(items))
	}
	if !items[0].Current || items[0].DeviceName == "" || items[0].IPAddress != "203.0.113.19" || items[0].IPPrefix != "203.0.113.0/24" {
		t.Fatalf("unexpected listed session: %#v", items[0])
	}
}

func TestServiceKeepsAtMostTenActiveSessions(t *testing.T) {
	sessions, user := newTestService(t)
	for index := 0; index < MaxActiveSessions+3; index++ {
		sessions.now = func() time.Time {
			return time.Date(2026, 7, 20, 10, index, 0, 0, time.UTC)
		}
		if _, err := sessions.Create(user.UUID, KindWeb); err != nil {
			t.Fatalf("create session %d: %v", index, err)
		}
	}
	items, err := sessions.List(user.UUID, "")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(items) != MaxActiveSessions {
		t.Fatalf("expected %d active sessions, got %d", MaxActiveSessions, len(items))
	}
}
