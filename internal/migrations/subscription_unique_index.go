package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func RunSubscriptionUniqueIndex(db *gorm.DB) error {
	if !db.Migrator().HasTable("subscriptions") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := DeduplicateSubscriptions(tx); err != nil {
			return err
		}
		if err := tx.Exec(`DROP INDEX IF EXISTS idx_subscriptions_user_source`).Error; err != nil {
			return fmt.Errorf("drop subscriptions unique index: %w", err)
		}
		if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_subscriptions_user_source
			ON subscriptions (user_id, feed_source_id)
			WHERE deleted_at IS NULL`).Error; err != nil {
			return fmt.Errorf("create subscriptions unique index: %w", err)
		}
		return nil
	})
}
