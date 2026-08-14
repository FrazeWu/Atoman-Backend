package testdb

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func Open(t *testing.T) *gorm.DB {
	return OpenWithConfig(t, &gorm.Config{})
}

// OpenWithConfig creates an isolated PostgreSQL schema for each test.
func OpenWithConfig(t *testing.T, config *gorm.Config) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("TEST_POSTGRES_DSN is required for PostgreSQL-backed tests")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	sqlDB, err := admin.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL connection: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create PostgreSQL test schema: %v", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), config)
	if err != nil {
		t.Fatalf("open PostgreSQL test schema: %v", err)
	}
	testSQLDB, err := db.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL test connection: %v", err)
	}
	testSQLDB.SetMaxOpenConns(2)
	testSQLDB.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := testSQLDB.Close(); err != nil {
			t.Errorf("close PostgreSQL test connection: %v", err)
		}
		if err := admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop PostgreSQL test schema: %v", err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close PostgreSQL admin connection: %v", err)
		}
	})
	return db
}

func Migrate(t *testing.T, db *gorm.DB, models ...any) {
	t.Helper()
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}
