package config

import (
	"strings"
	"testing"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"ENV", "GIN_MODE", "PORT", "JWT_SECRET", "DATABASE_TYPE", "DATABASE_URL", "STORAGE_TYPE", "ALLOWED_ORIGINS"} {
		t.Setenv(key, "")
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	clearConfigEnv(t)
	t.Setenv("JWT_SECRET", "test-secret")
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
	if cfg.JWTSecret != "test-secret" {
		t.Fatalf("JWTSecret = %q, want test-secret", cfg.JWTSecret)
	}
	if cfg.DB.Type != "postgres" || cfg.DB.URL != "postgres://user:pass@localhost:5432/db" {
		t.Fatalf("DB = %#v, want postgres URL", cfg.DB)
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
}

func TestLoadRequiresJWTSecret(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("DATABASE_TYPE", "postgres")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Fatalf("Load() error = %v, want JWT_SECRET required", err)
	}
}

func TestLoadRequiresDatabaseType(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_TYPE") {
		t.Fatalf("Load() error = %v, want DATABASE_TYPE required", err)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("DATABASE_TYPE", "postgres")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("Load() error = %v, want DATABASE_URL required", err)
	}
}

func TestLoadAppendsAllowedOriginsInProduction(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ENV", "production")
	t.Setenv("ALLOWED_ORIGINS", "https://atoman.example, https://studio.example ,,https://api.example")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := append(DefaultAllowedOrigins(), "https://atoman.example", "https://studio.example", "https://api.example")
	assertOrigins(t, cfg.AllowedOrigins, want)
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
