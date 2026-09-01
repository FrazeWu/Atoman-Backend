package migrations

import (
	"fmt"

	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunBlogRatingContentMigration moves Blog rating runtime identity to content_entries.
// The legacy post_id column remains temporarily for non-Blog migration dependencies.
func RunBlogRatingContentMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.PostRating{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&model.PostRating{}, "ContentID") {
		if err := db.Migrator().AddColumn(&model.PostRating{}, "ContentID"); err != nil {
			return fmt.Errorf("add post_ratings.content_id: %w", err)
		}
	}

	hasLegacyPostID := db.Migrator().HasColumn("post_ratings", "post_id")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if hasLegacyPostID {
			if err := tx.Exec(`
UPDATE post_ratings
SET content_id = post_id
WHERE content_id IS NULL`).Error; err != nil {
				return fmt.Errorf("backfill post_ratings.content_id: %w", err)
			}
		}

		var missing int64
		if err := tx.Table("post_ratings AS ratings").
			Joins("LEFT JOIN content_entries AS entries ON entries.id = ratings.content_id AND entries.kind = ? AND entries.deleted_at IS NULL", "blog").
			Where("ratings.deleted_at IS NULL AND entries.id IS NULL").
			Count(&missing).Error; err != nil {
			return fmt.Errorf("validate post rating content mapping: %w", err)
		}
		if missing != 0 {
			return fmt.Errorf("post rating content migration: %d active ratings have no canonical blog content entry", missing)
		}

		if hasLegacyPostID {
			if err := tx.Exec("ALTER TABLE post_ratings ALTER COLUMN post_id DROP NOT NULL").Error; err != nil {
				return fmt.Errorf("allow legacy post_ratings.post_id to be null: %w", err)
			}
		}

		if err := tx.Exec("DROP INDEX IF EXISTS idx_post_ratings_user_post").Error; err != nil {
			return fmt.Errorf("drop legacy post rating index: %w", err)
		}
		if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_post_ratings_user_content
			ON post_ratings (user_id, content_id)
			WHERE deleted_at IS NULL`).Error; err != nil {
			return fmt.Errorf("create canonical post rating index: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("blog rating content migration: %w", err)
	}
	return nil
}
