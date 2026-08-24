package migrations

import (
	"fmt"

	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunBlogBookmarkContentMigration makes content_entries the only Blog bookmark identity.
// The post_id column is retained temporarily for migration dependencies outside Blog.
func RunBlogBookmarkContentMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.Bookmark{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&model.Bookmark{}, "ContentID") {
		if err := db.Migrator().AddColumn(&model.Bookmark{}, "ContentID"); err != nil {
			return fmt.Errorf("add bookmarks.content_id: %w", err)
		}
	}

	hasLegacyPostID := db.Migrator().HasColumn("bookmarks", "post_id")
	return db.Transaction(func(tx *gorm.DB) error {
		if hasLegacyPostID {
			if err := tx.Exec(`UPDATE bookmarks SET content_id = post_id WHERE content_id IS NULL`).Error; err != nil {
				return fmt.Errorf("backfill bookmarks.content_id: %w", err)
			}
		}

		var missing int64
		if err := tx.Table("bookmarks AS bookmarks").
			Joins("LEFT JOIN content_entries AS entries ON entries.id = bookmarks.content_id AND entries.kind = ? AND entries.deleted_at IS NULL", "blog").
			Where("bookmarks.deleted_at IS NULL AND entries.id IS NULL").Count(&missing).Error; err != nil {
			return fmt.Errorf("validate bookmark content mapping: %w", err)
		}
		if missing != 0 {
			return fmt.Errorf("blog bookmark content migration: %d active bookmarks have no canonical blog content entry", missing)
		}

		if err := tx.Exec("DROP INDEX IF EXISTS idx_bookmarks_user_post").Error; err != nil {
			return fmt.Errorf("drop legacy bookmark index: %w", err)
		}
		if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_bookmarks_user_content
			ON bookmarks (user_id, content_id) WHERE deleted_at IS NULL`).Error; err != nil {
			return fmt.Errorf("create canonical bookmark index: %w", err)
		}
		return nil
	})
}
