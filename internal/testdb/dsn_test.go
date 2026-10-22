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

func TestTestPostgresDSNFromEnvFileIgnoresDatabaseURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env.dev")
	if err := os.WriteFile(path, []byte("DATABASE_URL=postgres://test.example/development\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := testPostgresDSNFromEnvFile(path); got != "" {
		t.Fatalf("testPostgresDSNFromEnvFile() = %q, must not use the development database", got)
	}
}

func TestTestPostgresDSNFromGitCommonRoot(t *testing.T) {
	root := t.TempDir()
	mainRoot := filepath.Join(root, "checkout")
	worktreeRoot := filepath.Join(root, "worktree")
	gitDir := filepath.Join(mainRoot, ".git", "worktrees", "task")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeRoot, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainRoot, ".env.dev"), []byte("TEST_POSTGRES_DSN=postgres://test.example/worktree-fallback\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := testPostgresDSNFromGitCommonRoot(worktreeRoot); got != "postgres://test.example/worktree-fallback" {
		t.Fatalf("testPostgresDSNFromGitCommonRoot() = %q, want shared checkout configuration", got)
	}
}

func TestTestPostgresDSNDefaultsToLocalService(t *testing.T) {
	t.Setenv("TEST_POSTGRES_DSN", "")
	t.Setenv("TEST_DATABASE_URL", "")
	t.Chdir(t.TempDir())
	t.Setenv("DATABASE_URL", "postgres://production.example/do-not-use")
	if got := TestPostgresDSN(); got != LocalPostgresDSN {
		t.Fatalf("TestPostgresDSN() = %q, want local test service", got)
	}
}

func TestOpenUsesIsolatedSchema(t *testing.T) {
	db := Open(t)
	var schema string
	if err := db.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		t.Fatalf("query current schema: %v", err)
	}
	if !strings.HasPrefix(schema, "test_") {
		t.Fatalf("current schema = %q, want an isolated test schema", schema)
	}
}
