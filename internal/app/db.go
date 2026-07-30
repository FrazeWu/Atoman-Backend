package app

import (
	"fmt"
	"strings"

	"atoman/internal/config"

	_ "github.com/lib/pq" // PostgreSQL array type support
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func OpenDB(cfg config.DBConfig) (*gorm.DB, error) {
	dbType := strings.TrimSpace(strings.ToLower(cfg.Type))
	dbURL := strings.TrimSpace(cfg.URL)

	var dialector gorm.Dialector
	switch dbType {
	case "postgres", "postgresql":
		dialector = postgres.Open(dbURL)
	default:
		return nil, fmt.Errorf("unsupported database type %q (expected postgres)", cfg.Type)
	}

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", dbType, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("access %s connection pool: %w", dbType, err)
	}
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = config.DefaultDBMaxOpen
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = config.DefaultDBMaxIdle
	}
	maxLifetime := cfg.ConnMaxLifetime
	if maxLifetime <= 0 {
		maxLifetime = config.DefaultDBLifetime
	}
	maxIdleTime := cfg.ConnMaxIdleTime
	if maxIdleTime <= 0 {
		maxIdleTime = config.DefaultDBIdleTime
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetConnMaxLifetime(maxLifetime)
	sqlDB.SetConnMaxIdleTime(maxIdleTime)
	return db, nil
}
