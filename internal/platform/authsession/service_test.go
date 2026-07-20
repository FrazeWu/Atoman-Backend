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
