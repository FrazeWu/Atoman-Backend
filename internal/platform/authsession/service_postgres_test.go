package authsession

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestWebSessionLifecycleInPostgres(t *testing.T) {
	db := testdb.OpenPostgres(t, "auth_session")
	if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}); err != nil {
		t.Fatalf("migrate auth session schema: %v", err)
	}
	user := model.User{Username: "postgres-session-user", Email: "postgres-session@example.test", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	sessions := New(db)
	credentials, err := sessions.Create(user.UUID, KindWeb)
	if err != nil {
		t.Fatalf("create web session: %v", err)
	}
	resolved, err := sessions.Authenticate(credentials.Token, KindWeb)
	if err != nil {
		t.Fatalf("authenticate web session: %v", err)
	}
	if resolved.User.UUID != user.UUID || !sessions.VerifyCSRF(resolved.Session, credentials.CSRFToken) {
		t.Fatalf("unexpected resolved PostgreSQL session: %#v", resolved)
	}
	if err := sessions.Revoke(credentials.Token); err != nil {
		t.Fatalf("revoke web session: %v", err)
	}
	if _, err := sessions.Authenticate(credentials.Token, KindWeb); err == nil {
		t.Fatal("revoked PostgreSQL session remained valid")
	}
}
