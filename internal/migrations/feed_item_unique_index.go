package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func RunFeedItemUniqueIndex(db *gorm.DB) error {
	if !db.Migrator().HasTable("feed_items") {
		return nil
	}

	if err := deduplicateFeedItems(db); err != nil {
		return err
	}

	return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_feed_items_source_guid
		ON feed_items (feed_source_id, guid)`).Error
}

func deduplicateFeedItems(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "postgres":
		return db.Exec(`
DELETE FROM feed_items fi
USING (
  SELECT ctid
  FROM (
    SELECT ctid,
           ROW_NUMBER() OVER (
             PARTITION BY feed_source_id, guid
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM feed_items
  ) ranked
  WHERE ranked.row_num > 1
) duplicates
WHERE fi.ctid = duplicates.ctid;
`).Error
	case "sqlite":
		return db.Exec(`
DELETE FROM feed_items
WHERE rowid IN (
  SELECT rowid
  FROM (
    SELECT rowid,
           ROW_NUMBER() OVER (
             PARTITION BY feed_source_id, guid
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM feed_items
  )
  WHERE row_num > 1
);
`).Error
	default:
		return fmt.Errorf("unsupported dialect for feed item dedupe: %s", db.Dialector.Name())
	}
}
