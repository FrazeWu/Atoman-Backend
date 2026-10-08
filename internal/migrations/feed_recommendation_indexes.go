package migrations

import "gorm.io/gorm"

func RunFeedRecommendationIndexes(db *gorm.DB) error {
	if db.Migrator().HasTable("feed_items") {
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_feed_items_source_recent
			ON feed_items (feed_source_id, published_at DESC, created_at DESC)
			WHERE deleted_at IS NULL`).Error; err != nil {
			return err
		}
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_feed_items_recommendation_published
			ON feed_items (published_at DESC, id DESC) WHERE deleted_at IS NULL`).Error; err != nil {
			return err
		}
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_feed_items_recommendation_source_language_published
			ON feed_items (feed_source_id, language_code, published_at DESC, id DESC)
			WHERE deleted_at IS NULL`).Error; err != nil {
			return err
		}
	}
	if db.Migrator().HasTable("feed_sources") {
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_feed_sources_curated_title
			ON feed_sources (LOWER(TRIM(title)))
			WHERE source_type = 'external_rss' AND hidden = false AND deleted_at IS NULL`).Error; err != nil {
			return err
		}
	}
	if db.Migrator().HasTable("subscriptions") {
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_subscriptions_source_live
			ON subscriptions (feed_source_id) WHERE deleted_at IS NULL`).Error; err != nil {
			return err
		}
	}
	return nil
}
