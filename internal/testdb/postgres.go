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

func OpenPostgres(t *testing.T, schemaPrefix string) *gorm.DB {
	t.Helper()
	dsn := TestPostgresDSN()
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured; set it or create .env.dev from .env.example")
	}
	if templateName := os.Getenv("TEST_POSTGRES_TEMPLATE_DATABASE"); templateName != "" {
		return openTemplateDatabase(t, dsn, schemaPrefix, templateName)
	}

	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open PostgreSQL admin connection: %v", err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL admin connection: %v", err)
	}
	if err := adminSQL.Ping(); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}

	schema := schemaPrefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL test schema: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL test connection: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
		_ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		_ = adminSQL.Close()
	})
	return db
}
