package app

import (
	"strings"
	"testing"

	"atoman/internal/config"
)

func TestOpenDBRejectsSQLite(t *testing.T) {
	_, err := OpenDB(config.DBConfig{Type: "sqlite", URL: ":memory:"})
	if err == nil {
		t.Fatal("OpenDB(sqlite) error = nil, want unsupported type error")
	}
	if !strings.Contains(err.Error(), "expected postgres") {
		t.Fatalf("OpenDB(sqlite) error = %v, want postgres-only message", err)
	}
}

func TestOpenDBRejectsUnknownType(t *testing.T) {
	_, err := OpenDB(config.DBConfig{Type: "mysql", URL: "dsn"})
	if err == nil {
		t.Fatal("OpenDB(mysql) error = nil, want unsupported type error")
	}
	if !strings.Contains(err.Error(), "unsupported database type") {
		t.Fatalf("OpenDB(mysql) error = %v, want unsupported database type", err)
	}
}

func TestOpenDBPostgresReturnsConnectionErrorForInvalidDSN(t *testing.T) {
	_, err := OpenDB(config.DBConfig{Type: "postgresql", URL: "not a valid dsn"})
	if err == nil {
		t.Fatal("OpenDB(postgresql invalid dsn) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "open postgresql database") {
		t.Fatalf("OpenDB(postgresql invalid dsn) error = %v, want wrapped open error", err)
	}
}
