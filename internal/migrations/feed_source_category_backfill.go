package migrations

import (
	"atoman/internal/feedclass"

	"gorm.io/gorm"
)

func RunFeedSourceCategoryBackfill(db *gorm.DB) error {
	if !db.Migrator().HasTable("feed_sources") {
		return nil
	}

	changes, err := feedclass.CollectChanges(db)
	if err != nil {
		return err
	}
	return feedclass.ApplyChanges(db, changes)
}
