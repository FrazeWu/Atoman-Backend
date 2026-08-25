package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunFeedCrawlOptimizationMigration adds durable state used to coordinate
// full-text workers and adapt RSS polling without changing public feed data.
func RunFeedCrawlOptimizationMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable("feed_sources") || !db.Migrator().HasTable("feed_items") {
		return nil
	}

	timestampType := "TIMESTAMPTZ"
	if db.Dialector.Name() == "sqlite" {
		timestampType = "DATETIME"
	}

	sourceColumns := []string{
		`fetch_unchanged_count INTEGER NOT NULL DEFAULT 0`,
		`full_text_lease_token VARCHAR(128)`,
		"full_text_lease_until " + timestampType,
	}
	for _, column := range sourceColumns {
		if err := db.Exec("ALTER TABLE feed_sources ADD COLUMN IF NOT EXISTS " + column).Error; err != nil {
			return err
		}
	}
	if err := db.Exec(`ALTER TABLE feed_items ADD COLUMN IF NOT EXISTS full_text_url_hash VARCHAR(64)`).Error; err != nil {
		return err
	}

	if err := db.AutoMigrate(&model.FeedFullTextHost{}); err != nil {
		return err
	}

	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_feed_sources_fulltext_lease
		ON feed_sources (full_text_lease_until, full_text_enabled, source_type)`).Error; err != nil {
		return err
	}
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_feed_fulltext_hosts_ready
		ON feed_fulltext_hosts (next_allowed_at, lease_until)`).Error; err != nil {
		return err
	}
	return db.Exec(`CREATE INDEX IF NOT EXISTS idx_feed_items_fulltext_url_hash
		ON feed_items (full_text_url_hash, full_text_status, full_text_fetched_at DESC)
		WHERE deleted_at IS NULL AND full_text_url_hash <> ''`).Error
}
