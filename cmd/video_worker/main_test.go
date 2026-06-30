package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDatabaseLogTargetRedactsCredentialsAndQuery(t *testing.T) {
	rawURL := "postgres://user:secret@db.example.com:5432/atoman?sslmode=require&password=leak"

	got := databaseLogTarget("postgres", rawURL)

	for _, leaked := range []string{"user", "secret", "sslmode", "password", "require", "leak", rawURL} {
		if strings.Contains(got, leaked) {
			t.Fatalf("databaseLogTarget leaked %q in %q", leaked, got)
		}
	}
	if !strings.Contains(got, "postgres") {
		t.Fatalf("databaseLogTarget() = %q, want database type", got)
	}
	if !strings.Contains(got, "host=db.example.com:5432") {
		t.Fatalf("databaseLogTarget() = %q, want host", got)
	}
	if !strings.Contains(got, "dbname=atoman") {
		t.Fatalf("databaseLogTarget() = %q, want dbname", got)
	}
}

func TestDatabaseConnectionLogRedactsURLSecrets(t *testing.T) {
	rawURL := "postgres://dbuser:dbsecret@db.example.com:5432/atoman?sslmode=require&password=leak"

	line := "Connecting to " + databaseLogTarget("postgres", rawURL)

	for _, leaked := range []string{"dbuser", "dbsecret", "sslmode", "password", "require", "leak", rawURL} {
		if strings.Contains(line, leaked) {
			t.Fatalf("database connection log leaked %q in %q", leaked, line)
		}
	}
	if !strings.Contains(line, "Connecting to postgres database") {
		t.Fatalf("database connection log = %q, want database type", line)
	}
	if !strings.Contains(line, "host=db.example.com:5432") {
		t.Fatalf("database connection log = %q, want host", line)
	}
	if !strings.Contains(line, "dbname=atoman") {
		t.Fatalf("database connection log = %q, want dbname", line)
	}
}

func TestDatabaseLogTargetRedactsKeywordDSN(t *testing.T) {
	rawURL := "host=localhost port=5432 user=atoman password=secret dbname=studio sslmode=disable"

	got := databaseLogTarget("postgres", rawURL)

	for _, leaked := range []string{"user", "atoman", "password", "secret", "sslmode", "disable", rawURL} {
		if strings.Contains(got, leaked) {
			t.Fatalf("databaseLogTarget leaked %q in %q", leaked, got)
		}
	}
	if !strings.Contains(got, "host=localhost:5432") {
		t.Fatalf("databaseLogTarget() = %q, want host with port", got)
	}
	if !strings.Contains(got, "dbname=studio") {
		t.Fatalf("databaseLogTarget() = %q, want dbname", got)
	}
}

func TestUploadsRootFromEnvUsesConfiguredExistingDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	got, err := uploadsRootFromEnv()

	if err != nil {
		t.Fatalf("uploadsRootFromEnv() error = %v", err)
	}
	if got != root {
		t.Fatalf("uploadsRootFromEnv() = %q, want %q", got, root)
	}
}

func TestUploadsRootFromEnvRequiresExplicitConfig(t *testing.T) {
	t.Setenv("UPLOADS_ROOT", "")

	_, err := uploadsRootFromEnv()

	if err == nil {
		t.Fatal("uploadsRootFromEnv() error = nil, want required UPLOADS_ROOT error")
	}
	if !strings.Contains(err.Error(), "UPLOADS_ROOT is required") {
		t.Fatalf("uploadsRootFromEnv() error = %v, want required UPLOADS_ROOT error", err)
	}
	if strings.Contains(err.Error(), "/home/fa/") {
		t.Fatalf("uploadsRootFromEnv() error = %v, must not use machine-specific default path", err)
	}
}

func TestUploadsRootFromEnvRejectsMissingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("UPLOADS_ROOT", missing)

	_, err := uploadsRootFromEnv()

	if err == nil {
		t.Fatal("uploadsRootFromEnv() error = nil, want missing directory error")
	}
	if !os.IsNotExist(err) {
		t.Fatalf("uploadsRootFromEnv() error = %v, want not-exist error", err)
	}
}
