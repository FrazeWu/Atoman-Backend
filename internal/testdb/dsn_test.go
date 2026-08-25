package testdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTestPostgresDSN(t *testing.T) {
	t.Run("prefers the explicit test DSN", func(t *testing.T) {
		t.Setenv("TEST_POSTGRES_DSN", "postgres://test.example/explicit")
		t.Setenv("TEST_DATABASE_URL", "postgres://test.example/legacy")
		if got := TestPostgresDSN(); got != "postgres://test.example/explicit" {
			t.Fatalf("TestPostgresDSN() = %q, want explicit test DSN", got)
		}
	})

	t.Run("accepts the legacy test database URL", func(t *testing.T) {
		t.Setenv("TEST_POSTGRES_DSN", "")
		t.Setenv("TEST_DATABASE_URL", "postgres://test.example/legacy")
		if got := TestPostgresDSN(); got != "postgres://test.example/legacy" {
			t.Fatalf("TestPostgresDSN() = %q, want legacy test DSN", got)
		}
	})
}

func TestTestPostgresDSNFromEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env.dev")
	contents := "DATABASE_URL=postgres://test.example/development\nTEST_DATABASE_URL=postgres://test.example/legacy\nexport TEST_POSTGRES_DSN='postgres://test.example/explicit'\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := testPostgresDSNFromEnvFile(path); got != "postgres://test.example/explicit" {
		t.Fatalf("testPostgresDSNFromEnvFile() = %q, want explicit test DSN", got)
	}
}

func TestOpenReadsProjectDevDSN(t *testing.T) {
	t.Setenv("TEST_POSTGRES_DSN", "")
	t.Setenv("TEST_DATABASE_URL", "")
	if testPostgresDSNFromDevEnv() == "" {
		t.Skip("project .env.dev does not configure a test PostgreSQL DSN")
	}

	db := Open(t)
	var schema string
	if err := db.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		t.Fatalf("query current schema: %v", err)
	}
	if !strings.HasPrefix(schema, "test_") {
		t.Fatalf("current schema = %q, want an isolated test schema", schema)
	}
}
