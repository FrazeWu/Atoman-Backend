package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

type legacyAuthSession struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey"`
	UserID    uuid.UUID  `gorm:"type:uuid;not null;index"`
	TokenHash string     `gorm:"size:64;not null;uniqueIndex"`
	CSRFHash  string     `gorm:"size:64;not null;default:''"`
	Kind      string     `gorm:"size:16;not null;index"`
	ExpiresAt time.Time  `gorm:"not null;index"`
	RevokedAt *time.Time `gorm:"index"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (legacyAuthSession) TableName() string {
	return "auth_sessions"
}

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

func TestRunAuthSecurityMigrationBackfillsExistingSessions(t *testing.T) {
	db := testdb.Open(t)
	if err := db.AutoMigrate(&model.User{}, &legacyAuthSession{}); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	legacy := legacyAuthSession{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		TokenHash: "token-hash",
		Kind:      "web",
		ExpiresAt: createdAt.Add(24 * time.Hour),
		CreatedAt: createdAt,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}

	if err := RunAuthSecurityMigration(db); err != nil {
		t.Fatalf("migrate existing auth sessions: %v", err)
	}

	var migrated model.AuthSession
	if err := db.First(&migrated, "id = ?", legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !migrated.LastActiveAt.Equal(createdAt) {
		t.Fatalf("last_active_at = %s, want %s", migrated.LastActiveAt, createdAt)
	}
	if migrated.UserAgent != "" || migrated.IPPrefix != "" {
		t.Fatalf("expected empty legacy device metadata, got user_agent=%q ip_prefix=%q", migrated.UserAgent, migrated.IPPrefix)
	}
}
