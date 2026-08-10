package migrations

import "gorm.io/gorm"

func RunFeedRecommendationIndexes(db *gorm.DB) error {
	if !db.Migrator().HasTable("feed_items") {
		return nil
	}
	return db.Exec(`CREATE INDEX IF NOT EXISTS idx_feed_items_recommendation_published
		ON feed_items (published_at DESC, id DESC) WHERE deleted_at IS NULL`).Error
}
