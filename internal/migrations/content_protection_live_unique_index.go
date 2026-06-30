package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func RunContentProtectionLiveUniqueIndex(db *gorm.DB) error {
	if !db.Migrator().HasTable("content_protections") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`DROP INDEX IF EXISTS idx_content_protections_content_id`).Error; err != nil {
			return fmt.Errorf("drop legacy content protection unique index: %w", err)
		}
		if err := tx.Exec(`DROP INDEX IF EXISTS idx_content_protections_live_content`).Error; err != nil {
			return fmt.Errorf("drop existing content protection live index: %w", err)
		}
		if err := deduplicateLiveContentProtections(tx); err != nil {
			return err
		}
		if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_content_protections_live_content
			ON content_protections (content_type, content_id)
			WHERE deleted_at IS NULL`).Error; err != nil {
			return fmt.Errorf("create content protection live unique index: %w", err)
		}

		return nil
	})
}

func deduplicateLiveContentProtections(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "postgres":
		return db.Exec(`
DELETE FROM content_protections cp
USING (
  SELECT ctid
  FROM (
    SELECT ctid,
           ROW_NUMBER() OVER (
             PARTITION BY content_type, content_id
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM content_protections
    WHERE deleted_at IS NULL
  ) ranked
  WHERE ranked.row_num > 1
) duplicates
WHERE cp.ctid = duplicates.ctid;
`).Error
	case "sqlite":
		return db.Exec(`
DELETE FROM content_protections
WHERE rowid IN (
  SELECT rowid
  FROM (
    SELECT rowid,
           ROW_NUMBER() OVER (
             PARTITION BY content_type, content_id
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM content_protections
    WHERE deleted_at IS NULL
  )
  WHERE row_num > 1
);
`).Error
	default:
		return fmt.Errorf("unsupported dialect for content protection dedupe: %s", db.Dialector.Name())
	}
}
