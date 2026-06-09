package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func Migrate20260603FeedSourceManagementMVP(db *gorm.DB) error {
	if !db.Migrator().HasTable("feed_sources") {
		return nil
	}

	type columnMigration struct {
		name string
		sql  string
	}

	migrations := []columnMigration{
		{name: "provider", sql: `ALTER TABLE feed_sources ADD COLUMN provider TEXT NOT NULL DEFAULT 'rss'`},
		{name: "canonical_url", sql: `ALTER TABLE feed_sources ADD COLUMN canonical_url TEXT`},
		{name: "site_url", sql: `ALTER TABLE feed_sources ADD COLUMN site_url TEXT`},
		{name: "hidden", sql: `ALTER TABLE feed_sources ADD COLUMN hidden BOOLEAN NOT NULL DEFAULT FALSE`},
		{name: "health_status", sql: `ALTER TABLE feed_sources ADD COLUMN health_status TEXT NOT NULL DEFAULT 'healthy'`},
		{name: "last_error", sql: `ALTER TABLE feed_sources ADD COLUMN last_error TEXT`},
	}

	return db.Transaction(func(tx *gorm.DB) error {
		for _, migration := range migrations {
			if tx.Migrator().HasColumn("feed_sources", migration.name) {
				continue
			}
			if err := tx.Exec(migration.sql).Error; err != nil {
				return fmt.Errorf("add feed_sources.%s: %w", migration.name, err)
			}
		}
		return nil
	})
}
