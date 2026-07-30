package config

import (
	"strings"
	"testing"
	"time"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"ENV", "GIN_MODE", "PORT", "AUTH_CODE_SECRET", "DATABASE_TYPE", "DATABASE_URL", "DATABASE_MAX_OPEN_CONNS", "DATABASE_MAX_IDLE_CONNS", "DATABASE_CONN_MAX_LIFETIME", "DATABASE_CONN_MAX_IDLE_TIME", "STORAGE_TYPE", "ALLOWED_ORIGINS"} {
		t.Setenv(key, "")
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	clearConfigEnv(t)
	t.Setenv("DATABASE_TYPE", "postgres")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
}

func TestLoadUsesDefaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Env != DefaultEnv {
		t.Fatalf("Env = %q, want %q", cfg.Env, DefaultEnv)
	}
	if cfg.GinMode != DefaultGinMode {
		t.Fatalf("GinMode = %q, want %q", cfg.GinMode, DefaultGinMode)
	}
	if cfg.Port != DefaultPort {
		t.Fatalf("Port = %q, want %q", cfg.Port, DefaultPort)
	}
	if cfg.StorageType != DefaultStorageType {
		t.Fatalf("StorageType = %q, want %q", cfg.StorageType, DefaultStorageType)
	}
	if cfg.DB.Type != "postgres" || cfg.DB.URL != "postgres://user:pass@localhost:5432/db" {
		t.Fatalf("DB = %#v, want postgres URL", cfg.DB)
	}
	if cfg.DB.MaxOpenConns != 20 || cfg.DB.MaxIdleConns != 10 {
		t.Fatalf("DB pool = %#v, want open=20 idle=10", cfg.DB)
	}
	if cfg.DB.ConnMaxLifetime != 30*time.Minute || cfg.DB.ConnMaxIdleTime != 5*time.Minute {
		t.Fatalf("DB pool durations = %#v", cfg.DB)
	}
	assertOrigins(t, cfg.AllowedOrigins, DefaultAllowedOrigins())
}

func TestLoadReadsConfiguredValues(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ENV", "staging")
	t.Setenv("GIN_MODE", "release")
	t.Setenv("PORT", "9000")
	t.Setenv("DATABASE_TYPE", "postgres")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("STORAGE_TYPE", "s3")
	t.Setenv("DATABASE_MAX_OPEN_CONNS", "12")
	t.Setenv("DATABASE_MAX_IDLE_CONNS", "6")
	t.Setenv("DATABASE_CONN_MAX_LIFETIME", "20m")
	t.Setenv("DATABASE_CONN_MAX_IDLE_TIME", "3m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Env != "staging" {
		t.Fatalf("Env = %q, want staging", cfg.Env)
	}
	if cfg.GinMode != "release" {
		t.Fatalf("GinMode = %q, want release", cfg.GinMode)
	}
	if cfg.Port != "9000" {
		t.Fatalf("Port = %q, want 9000", cfg.Port)
	}
	if cfg.DB.Type != "postgres" {
		t.Fatalf("DB.Type = %q, want postgres", cfg.DB.Type)
	}
	if cfg.DB.URL != "postgres://user:pass@localhost:5432/db" {
		t.Fatalf("DB.URL = %q", cfg.DB.URL)
	}
	if cfg.StorageType != "s3" {
		t.Fatalf("StorageType = %q, want s3", cfg.StorageType)
	}
	if cfg.DB.MaxOpenConns != 12 || cfg.DB.MaxIdleConns != 6 || cfg.DB.ConnMaxLifetime != 20*time.Minute || cfg.DB.ConnMaxIdleTime != 3*time.Minute {
		t.Fatalf("DB pool = %#v, want configured values", cfg.DB)
	}
}

func TestLoadRejectsInvalidDatabasePoolConfig(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DATABASE_MAX_OPEN_CONNS", "0")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_MAX_OPEN_CONNS") {
		t.Fatalf("Load() error = %v, want invalid pool configuration", err)
	}
}

func TestLoadRequiresDatabaseType(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_TYPE") {
		t.Fatalf("Load() error = %v, want DATABASE_TYPE required", err)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("DATABASE_TYPE", "postgres")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("Load() error = %v, want DATABASE_URL required", err)
	}
}

func TestLoadAppendsAllowedOriginsInProduction(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ENV", "production")
	t.Setenv("AUTH_CODE_SECRET", "production-auth-code-secret")
	t.Setenv("ALLOWED_ORIGINS", "https://atoman.example, https://studio.example ,,https://api.example")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := append(DefaultAllowedOrigins(), "https://atoman.example", "https://studio.example", "https://api.example")
	assertOrigins(t, cfg.AllowedOrigins, want)
}

func TestLoadRequiresAuthCodeSecretInProduction(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ENV", "production")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AUTH_CODE_SECRET") {
		t.Fatalf("Load() error = %v, want AUTH_CODE_SECRET required", err)
	}
}

func TestLoadIgnoresAllowedOriginsOutsideProduction(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ENV", "development")
	t.Setenv("ALLOWED_ORIGINS", "https://atoman.example")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	assertOrigins(t, cfg.AllowedOrigins, DefaultAllowedOrigins())
}

func assertOrigins(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("origins len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("origins[%d] = %q, want %q (all: %#v)", i, got[i], want[i], got)
		}
	}
}
