package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunAuthOAuthMigrationCreatesIdentityAndFlowConstraints(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{})

	if err := RunAuthOAuthMigration(db); err != nil {
		t.Fatalf("run oauth migration: %v", err)
	}
	if err := RunAuthOAuthMigration(db); err != nil {
		t.Fatalf("run oauth migration twice: %v", err)
	}

	user := model.User{Username: "oauth-user", Email: "oauth@example.com", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create oauth-only user: %v", err)
	}

	identity := model.ExternalIdentity{
		UserID:        user.UUID,
		Provider:      "google",
		Issuer:        "https://accounts.google.com",
		Subject:       "subject-1",
		Email:         "oauth@example.com",
		EmailVerified: true,
	}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatalf("create identity: %v", err)
	}

	duplicateSubject := identity
	duplicateSubject.UUID = [16]byte{}
	if err := db.Create(&duplicateSubject).Error; err == nil {
		t.Fatal("expected provider issuer subject to be unique")
	}

	secondIdentity := identity
	secondIdentity.UUID = [16]byte{}
	secondIdentity.Subject = "subject-2"
	if err := db.Create(&secondIdentity).Error; err == nil {
		t.Fatal("expected one identity per user and provider")
	}

	flow := model.OAuthFlow{
		SecretHash: "flow-secret-hash",
		Provider:   "google",
		Purpose:    "login",
		Stage:      "started",
		ExpiresAt:  time.Now().UTC().Add(10 * time.Minute),
	}
	if err := db.Create(&flow).Error; err != nil {
		t.Fatalf("create oauth flow: %v", err)
	}
	duplicateFlow := flow
	duplicateFlow.UUID = [16]byte{}
	if err := db.Create(&duplicateFlow).Error; err == nil {
		t.Fatal("expected oauth flow secret hash to be unique")
	}
}
