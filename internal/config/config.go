package config

import (
	"fmt"
	"os"
	"strings"
)

const (
	DefaultEnv         = "development"
	DefaultGinMode     = "debug"
	DefaultPort        = "8080"
	DefaultStorageType = "local"
)

var defaultAllowedOrigins = []string{
	"http://localhost:5173",
	"http://localhost:3000",
	"http://127.0.0.1:5173",
	"http://127.0.0.1:3000",
}

type Config struct {
	Env            string
	GinMode        string
	Port           string
	JWTSecret      string
	DB             DBConfig
	StorageType    string
	AllowedOrigins []string
}

type DBConfig struct {
	Type string
	URL  string
}

func Load() (Config, error) {
	cfg := Config{
		Env:            getEnv("ENV", DefaultEnv),
		GinMode:        getEnv("GIN_MODE", DefaultGinMode),
		Port:           getEnv("PORT", DefaultPort),
		JWTSecret:      os.Getenv("JWT_SECRET"),
		DB:             DBConfig{Type: os.Getenv("DATABASE_TYPE"), URL: os.Getenv("DATABASE_URL")},
		StorageType:    getEnv("STORAGE_TYPE", DefaultStorageType),
		AllowedOrigins: append([]string(nil), defaultAllowedOrigins...),
	}

	if cfg.JWTSecret == "" {
		return Config{}, fmt.Errorf("JWT_SECRET is required")
	}
	if cfg.DB.Type == "" {
		return Config{}, fmt.Errorf("DATABASE_TYPE is required")
	}
	if cfg.DB.URL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	if cfg.Env == "production" {
		cfg.AllowedOrigins = append(cfg.AllowedOrigins, parseCSV(os.Getenv("ALLOWED_ORIGINS"))...)
	}

	return cfg, nil
}

func DefaultAllowedOrigins() []string {
	return append([]string(nil), defaultAllowedOrigins...)
}

func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func parseCSV(value string) []string {
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		items = append(items, item)
	}
	return items
}
