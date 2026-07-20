package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunAuthSecurityMigrationCreatesSessionsAndCaseInsensitiveUserIndexes(t *testing.T) {
	db := testdb.Open(t)
	if err := db.AutoMigrate(&model.User{}, &model.EmailVerificationCode{}); err != nil {
		t.Fatal(err)
	}
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	legacyCode := model.EmailVerificationCode{
		Email: "legacy@example.com", Purpose: "registration", CodeHash: "123456",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := db.Create(&legacyCode).Error; err != nil {
		t.Fatal(err)
	}

	if err := RunAuthSecurityMigration(db); err != nil {
		t.Fatalf("run auth security migration: %v", err)
	}
	if !db.Migrator().HasTable(&model.AuthSession{}) {
		t.Fatal("expected auth_sessions table")
	}
	if !db.Migrator().HasColumn(&model.EmailVerificationCode{}, "failed_attempts") {
		t.Fatal("expected failed_attempts column")
	}
	var migrated model.EmailVerificationCode
	if err := db.First(&migrated, "uuid = ?", legacyCode.UUID).Error; err != nil {
		t.Fatal(err)
	}
	if !migrated.Used {
		t.Fatal("legacy plaintext verification codes must be invalidated")
	}
	duplicate := model.User{Username: "ALICE", Email: "other@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("expected case-insensitive username uniqueness violation")
	}
	duplicate = model.User{Username: "other", Email: "ALICE@EXAMPLE.COM", Password: "hash", IsActive: true}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("expected case-insensitive email uniqueness violation")
	}
}
