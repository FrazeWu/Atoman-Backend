package testdb

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func Open(t *testing.T) *gorm.DB {
	return OpenWithConfig(t, &gorm.Config{})
}

// OpenWithConfig creates an isolated in-memory SQLite database. PostgreSQL
// integration tests must opt in through OpenPostgres.
func OpenWithConfig(t *testing.T, config *gorm.Config) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), config)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func Migrate(t *testing.T, db *gorm.DB, models ...any) {
	t.Helper()
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}
