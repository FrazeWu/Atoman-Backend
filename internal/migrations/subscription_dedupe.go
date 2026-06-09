package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

// DeduplicateSubscriptions removes duplicate subscriptions for the same
// (user_id, feed_source_id) pair before the unique index is created.
// It keeps the earliest created row in each group.
func DeduplicateSubscriptions(db *gorm.DB) error {
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
             ORDER BY
               CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END,
               created_at DESC,
               id DESC
           ) AS row_num
    FROM subscriptions
  ) ranked
  WHERE ranked.row_num > 1
) duplicates
WHERE s.ctid = duplicates.ctid;
`).Error
	default:
		return fmt.Errorf("unsupported dialect for subscription dedupe: %s", db.Dialector.Name())
	}
}
