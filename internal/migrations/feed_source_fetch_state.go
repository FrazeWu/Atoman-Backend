package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunFeedSourceFetchStateMigration adds durable HTTP fetch state to existing
// feed sources. It is intentionally safe to run before the main schema pass so
// feed scheduling can be repaired even when an unrelated migration is blocked.
func RunFeedSourceFetchStateMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.FeedSource{}) {
		return nil
	}

	columnType := "TIMESTAMPTZ"
	if db.Dialector.Name() == "sqlite" {
		columnType = "DATETIME"
	}
	columns := []string{
		`fetch_status VARCHAR(24) NOT NULL DEFAULT 'idle'`,
		`fetch_provider VARCHAR(32)`,
		`fetch_http_status INTEGER NOT NULL DEFAULT 0`,
		`fetch_etag TEXT`,
		`fetch_last_modified TEXT`,
		"fetch_last_success_at " + columnType,
		"fetch_next_at " + columnType,
		`fetch_consecutive_failures INTEGER NOT NULL DEFAULT 0`,
		`fetch_last_error_code VARCHAR(64)`,
		`fetch_last_error TEXT`,
		`fetch_last_duration_ms BIGINT NOT NULL DEFAULT 0`,
		`fetch_last_item_count INTEGER NOT NULL DEFAULT 0`,
	}
	for _, column := range columns {
		if err := db.Exec("ALTER TABLE feed_sources ADD COLUMN IF NOT EXISTS " + column).Error; err != nil {
			return err
		}
	}
	if err := db.Model(&model.FeedSource{}).
		Where("fetch_status IS NULL OR fetch_status = ''").
		Update("fetch_status", "idle").Error; err != nil {
		return err
	}
	if db.Dialector.Name() == "postgres" {
		if err := db.Exec(`ALTER TABLE feed_sources DROP COLUMN IF EXISTS fetch_e_tag`).Error; err != nil {
			return err
		}
	}
	return db.Exec(`CREATE INDEX IF NOT EXISTS idx_feed_sources_fetch_schedule
		ON feed_sources (fetch_next_at, source_type, hidden)`).Error
}
