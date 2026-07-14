package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunUnifiedReadingListMigration(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if !tx.Migrator().HasTable("reading_list_items") {
			if err := tx.AutoMigrate(&model.ReadingListItem{}); err != nil {
				return err
			}
			return dropReadingListTargetConstraints(tx)
		}
		if tx.Migrator().HasColumn("reading_list_items", "target_type") && tx.Migrator().HasColumn("reading_list_items", "target_id") {
			return dropReadingListTargetConstraints(tx)
		}
		if !tx.Migrator().HasColumn("reading_list_items", "feed_item_id") {
			if err := tx.AutoMigrate(&model.ReadingListItem{}); err != nil {
				return err
			}
			return dropReadingListTargetConstraints(tx)
		}

		if err := tx.Migrator().RenameTable("reading_list_items", "reading_list_items_legacy"); err != nil {
			return err
		}
		if err := tx.AutoMigrate(&model.ReadingListItem{}); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO reading_list_items (user_id, target_type, target_id, created_at)
			SELECT user_id, 'feed_item', feed_item_id, created_at
			FROM reading_list_items_legacy
		`).Error; err != nil {
			return err
		}
		if err := tx.Migrator().DropTable("reading_list_items_legacy"); err != nil {
			return err
		}
		return dropReadingListTargetConstraints(tx)
	})
}

func dropReadingListTargetConstraints(db *gorm.DB) error {
	for _, name := range []string{"fk_reading_list_items_feed_item", "fk_reading_list_items_post"} {
		if db.Migrator().HasConstraint(&model.ReadingListItem{}, name) {
			if err := db.Migrator().DropConstraint(&model.ReadingListItem{}, name); err != nil {
				return err
			}
		}
	}
	return nil
}
