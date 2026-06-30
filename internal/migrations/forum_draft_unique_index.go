package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func RunForumDraftUniqueIndex(db *gorm.DB) error {
	if !db.Migrator().HasTable("forum_drafts") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := deduplicateForumDrafts(tx); err != nil {
			return err
		}

		if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_forum_drafts_user_context
			ON forum_drafts (user_id, context_key)`).Error; err != nil {
			return fmt.Errorf("create forum_drafts unique index: %w", err)
		}

		return nil
	})
}

func deduplicateForumDrafts(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "postgres", "pgx":
		if err := db.Exec(`
DELETE FROM forum_drafts draft
USING (
  SELECT ctid
  FROM (
    SELECT ctid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, context_key
             ORDER BY updated_at DESC, created_at DESC, id DESC
           ) AS row_num
    FROM forum_drafts
  ) ranked
  WHERE ranked.row_num > 1
) duplicates
WHERE draft.ctid = duplicates.ctid;
`).Error; err != nil {
			return fmt.Errorf("deduplicate forum drafts: %w", err)
		}
		return nil
	case "sqlite":
		if err := db.Exec(`
DELETE FROM forum_drafts
WHERE rowid IN (
  SELECT rowid
  FROM (
    SELECT rowid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, context_key
             ORDER BY updated_at DESC, created_at DESC, id DESC
           ) AS row_num
    FROM forum_drafts
  )
  WHERE row_num > 1
);
`).Error; err != nil {
			return fmt.Errorf("deduplicate forum drafts: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported dialect for forum draft dedupe: %s", db.Dialector.Name())
	}
}
