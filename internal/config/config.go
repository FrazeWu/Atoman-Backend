package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultEnv         = "development"
	DefaultGinMode     = "debug"
	DefaultPort        = "8080"
	DefaultStorageType = "local"
	DefaultDBMaxOpen   = 20
	DefaultDBMaxIdle   = 10
	DefaultDBLifetime  = 30 * time.Minute
	DefaultDBIdleTime  = 5 * time.Minute
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
	DB             DBConfig
	StorageType    string
	AllowedOrigins []string
}

type DBConfig struct {
	Type            string
	URL             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

func Load() (Config, error) {
	dbConfig, err := loadDBConfig()
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Env:            getEnv("ENV", DefaultEnv),
		GinMode:        getEnv("GIN_MODE", DefaultGinMode),
		Port:           getEnv("PORT", DefaultPort),
		DB:             dbConfig,
		StorageType:    getEnv("STORAGE_TYPE", DefaultStorageType),
		AllowedOrigins: append([]string(nil), defaultAllowedOrigins...),
	}

	if cfg.DB.Type == "" {
		return Config{}, fmt.Errorf("DATABASE_TYPE is required")
	}
	if cfg.DB.URL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	if cfg.Env == "production" {
		if strings.TrimSpace(os.Getenv("AUTH_CODE_SECRET")) == "" {
			return Config{}, fmt.Errorf("AUTH_CODE_SECRET is required")
		}
		cfg.AllowedOrigins = append(cfg.AllowedOrigins, parseCSV(os.Getenv("ALLOWED_ORIGINS"))...)
	}

	return cfg, nil
}

func loadDBConfig() (DBConfig, error) {
	maxOpen, err := positiveIntEnv("DATABASE_MAX_OPEN_CONNS", DefaultDBMaxOpen)
	if err != nil {
		return DBConfig{}, err
	}
	maxIdle, err := positiveIntEnv("DATABASE_MAX_IDLE_CONNS", DefaultDBMaxIdle)
	if err != nil {
		return DBConfig{}, err
	}
	maxLifetime, err := positiveDurationEnv("DATABASE_CONN_MAX_LIFETIME", DefaultDBLifetime)
	if err != nil {
		return DBConfig{}, err
	}
	maxIdleTime, err := positiveDurationEnv("DATABASE_CONN_MAX_IDLE_TIME", DefaultDBIdleTime)
	if err != nil {
		return DBConfig{}, err
	}
	return DBConfig{
		Type:            os.Getenv("DATABASE_TYPE"),
		URL:             os.Getenv("DATABASE_URL"),
		MaxOpenConns:    maxOpen,
		MaxIdleConns:    maxIdle,
		ConnMaxLifetime: maxLifetime,
		ConnMaxIdleTime: maxIdleTime,
	}, nil
}

func positiveIntEnv(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
}

func positiveDurationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return value, nil
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
