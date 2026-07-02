package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func RunBlogDefaultUniqueIndexes(db *gorm.DB) error {
	if !db.Migrator().HasTable("channels") && !db.Migrator().HasTable("collections") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if tx.Migrator().HasTable("channels") {
			if err := deduplicateDefaultChannels(tx); err != nil {
				return err
			}
			if err := tx.Exec(`DROP INDEX IF EXISTS idx_channels_user_default`).Error; err != nil {
				return fmt.Errorf("drop channel default unique index: %w", err)
			}
			if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_user_default
				ON channels (user_id)
				WHERE user_id IS NOT NULL AND is_default = TRUE`).Error; err != nil {
				return fmt.Errorf("create channel default unique index: %w", err)
			}
		}

		if tx.Migrator().HasTable("collections") {
			if err := deduplicateDefaultCollections(tx); err != nil {
				return err
			}
			if err := tx.Exec(`DROP INDEX IF EXISTS idx_collections_channel_default`).Error; err != nil {
				return fmt.Errorf("drop collection default unique index: %w", err)
			}
			if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_collections_channel_default
				ON collections (channel_id)
				WHERE is_default = TRUE`).Error; err != nil {
				return fmt.Errorf("create collection default unique index: %w", err)
			}
		}

		return nil
	})
}

func deduplicateDefaultChannels(db *gorm.DB) error {
	return db.Exec(`
UPDATE channels
SET is_default = FALSE
WHERE id IN (
	SELECT id
	FROM (
		SELECT id,
		       ROW_NUMBER() OVER (
		         PARTITION BY user_id
		         ORDER BY created_at ASC, id ASC
		       ) AS row_num
		FROM channels
		WHERE user_id IS NOT NULL AND is_default = TRUE
	)
	WHERE row_num > 1
)`).Error
}

func deduplicateDefaultCollections(db *gorm.DB) error {
	return db.Exec(`
UPDATE collections
SET is_default = FALSE
WHERE id IN (
	SELECT id
	FROM (
		SELECT id,
		       ROW_NUMBER() OVER (
		         PARTITION BY channel_id
		         ORDER BY created_at ASC, id ASC
		       ) AS row_num
		FROM collections
		WHERE is_default = TRUE
	)
	WHERE row_num > 1
)`).Error
}
