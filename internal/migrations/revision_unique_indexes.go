package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func RunRevisionUniqueIndexes(db *gorm.DB) error {
	if !db.Migrator().HasTable("revisions") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := deduplicateRevisionVersions(tx); err != nil {
			return err
		}
		if err := collapseRevisionCurrents(tx); err != nil {
			return err
		}
		if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_revisions_content_version
			ON revisions (content_type, content_id, version_number)`).Error; err != nil {
			return fmt.Errorf("create revision content version index: %w", err)
		}
		if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_revisions_current_content
			ON revisions (content_type, content_id)
			WHERE is_current = true`).Error; err != nil {
			return fmt.Errorf("create revision current content index: %w", err)
		}
		return nil
	})
}

func deduplicateRevisionVersions(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "postgres":
		return db.Exec(`
DELETE FROM revisions revision
USING (
  SELECT ctid
  FROM (
    SELECT ctid,
           ROW_NUMBER() OVER (
             PARTITION BY content_type, content_id, version_number
             ORDER BY is_current DESC, created_at DESC, id DESC
           ) AS row_num
    FROM revisions
  ) ranked
  WHERE ranked.row_num > 1
) duplicates
WHERE revision.ctid = duplicates.ctid;
`).Error
	case "sqlite":
		return db.Exec(`
DELETE FROM revisions
WHERE rowid IN (
  SELECT rowid
  FROM (
    SELECT rowid,
           ROW_NUMBER() OVER (
             PARTITION BY content_type, content_id, version_number
             ORDER BY is_current DESC, created_at DESC, id DESC
           ) AS row_num
    FROM revisions
  )
  WHERE row_num > 1
);
`).Error
	default:
		return fmt.Errorf("unsupported dialect for revision dedupe: %s", db.Dialector.Name())
	}
}

func collapseRevisionCurrents(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "postgres":
		return db.Exec(`
UPDATE revisions
SET is_current = false
WHERE ctid IN (
  SELECT ctid
  FROM (
    SELECT ctid,
           ROW_NUMBER() OVER (
             PARTITION BY content_type, content_id
             ORDER BY version_number DESC, created_at DESC, id DESC
           ) AS row_num
    FROM revisions
    WHERE is_current = true
  ) ranked
  WHERE ranked.row_num > 1
);
`).Error
	case "sqlite":
		return db.Exec(`
UPDATE revisions
SET is_current = false
WHERE rowid IN (
  SELECT rowid
  FROM (
    SELECT rowid,
           ROW_NUMBER() OVER (
             PARTITION BY content_type, content_id
             ORDER BY version_number DESC, created_at DESC, id DESC
           ) AS row_num
    FROM revisions
    WHERE is_current = true
  )
  WHERE row_num > 1
);
`).Error
	default:
		return fmt.Errorf("unsupported dialect for revision current cleanup: %s", db.Dialector.Name())
	}
}
