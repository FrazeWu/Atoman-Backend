package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func blogCollectionPostOrderBackfillQuery() string {
	return `
		SELECT post_id, collection_id
		FROM post_collections
		ORDER BY collection_id ASC, position ASC, post_id ASC
	`
}

func RunBlogCollectionPostOrderMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable("post_collections") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if !tx.Migrator().HasColumn("post_collections", "position") {
			if err := tx.Exec(`ALTER TABLE post_collections ADD COLUMN position INTEGER NOT NULL DEFAULT 0`).Error; err != nil {
				return fmt.Errorf("add post_collections.position: %w", err)
			}
		}

		type link struct {
			PostID       string
			CollectionID string
		}

		var links []link
		if err := tx.Raw(blogCollectionPostOrderBackfillQuery()).Scan(&links).Error; err != nil {
			return fmt.Errorf("load post_collections for position backfill: %w", err)
		}

		positions := make(map[string]int)
		for _, item := range links {
			key := item.CollectionID
			position := positions[key]
			if err := tx.Exec(
				`UPDATE post_collections SET position = ? WHERE post_id = ? AND collection_id = ?`,
				position,
				item.PostID,
				item.CollectionID,
			).Error; err != nil {
				return fmt.Errorf("backfill post_collections.position: %w", err)
			}
			positions[key] = position + 1
		}

		return nil
	})
}
