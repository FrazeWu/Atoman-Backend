package testdb

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func CreatePostgresTemplate(dsn, prefix string, models ...any) (string, func() error, error) {
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return "", nil, fmt.Errorf("open PostgreSQL admin connection: %w", err)
	}
	name := prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	cleanup := func() error {
		_ = admin.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = ?", name).Error
		err := admin.Exec("DROP DATABASE IF EXISTS " + quoteDatabaseIdentifier(name)).Error
		if sqlDB, dbErr := admin.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		return err
	}
	if err := admin.Exec("CREATE DATABASE " + quoteDatabaseIdentifier(name)).Error; err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("create template database: %w", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("parse PostgreSQL DSN: %w", err)
	}
	parsed.Path = "/" + name
	template, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("open template database: %w", err)
	}
	if err := template.AutoMigrate(models...); err != nil {
		if sqlDB, dbErr := template.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		_ = cleanup()
		return "", nil, fmt.Errorf("migrate template database: %w", err)
	}
	if sqlDB, err := template.DB(); err == nil {
		_ = sqlDB.Close()
	}
	return name, cleanup, nil
}

func openTemplateDatabase(t *testing.T, dsn, prefix, templateName string) *gorm.DB {
	t.Helper()
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL admin connection: %v", err)
	}
	name := prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE DATABASE " + quoteDatabaseIdentifier(name) + " TEMPLATE " + quoteDatabaseIdentifier(templateName)).Error; err != nil {
		t.Fatalf("clone PostgreSQL template database: %v", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL DSN: %v", err)
	}
	parsed.Path = "/" + name
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL clone: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL clone connection: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
		_ = admin.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = ?", name).Error
		_ = admin.Exec("DROP DATABASE IF EXISTS " + quoteDatabaseIdentifier(name)).Error
		if adminSQL, err := admin.DB(); err == nil {
			_ = adminSQL.Close()
		}
	})
	return db
}

func quoteDatabaseIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
