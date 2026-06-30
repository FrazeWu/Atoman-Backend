package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

// DeduplicateSubscriptions removes duplicate subscriptions for the same
// (user_id, feed_source_id) pair before the unique index is created.
// It keeps the newest live row in each group and preserves soft-deleted history.
func DeduplicateSubscriptions(db *gorm.DB) error {
	if !db.Migrator().HasTable("subscriptions") {
		return nil
	}

	switch db.Dialector.Name() {
	case "postgres":
		return db.Exec(`
DELETE FROM subscriptions s
USING (
  SELECT ctid
  FROM (
    SELECT ctid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, feed_source_id
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM subscriptions
    WHERE deleted_at IS NULL
  ) ranked
  WHERE ranked.row_num > 1
) duplicates
WHERE s.ctid = duplicates.ctid;
`).Error
	case "sqlite":
		return db.Exec(`
DELETE FROM subscriptions
WHERE rowid IN (
  SELECT rowid
  FROM (
    SELECT rowid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, feed_source_id
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM subscriptions
    WHERE deleted_at IS NULL
  )
  WHERE row_num > 1
);
`).Error
	default:
		return fmt.Errorf("unsupported dialect for subscription dedupe: %s", db.Dialector.Name())
	}
}
