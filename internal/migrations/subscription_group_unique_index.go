package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func RunSubscriptionGroupUniqueIndex(db *gorm.DB) error {
	if !db.Migrator().HasTable("subscription_groups") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := DeduplicateSubscriptionGroups(tx); err != nil {
			return err
		}
		if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_subscription_groups_user_name
			ON subscription_groups (user_id, name)
			WHERE deleted_at IS NULL`).Error; err != nil {
			return fmt.Errorf("create subscription groups unique index: %w", err)
		}
		return nil
	})
}

func DeduplicateSubscriptionGroups(db *gorm.DB) error {
	if !db.Migrator().HasTable("subscription_groups") {
		return nil
	}

	switch db.Dialector.Name() {
	case "postgres":
		if db.Migrator().HasTable("subscriptions") {
			if err := db.Exec(`
WITH ranked AS (
  SELECT id,
         FIRST_VALUE(id) OVER (
           PARTITION BY user_id, name
           ORDER BY created_at ASC, id ASC
         ) AS canonical_id,
         ROW_NUMBER() OVER (
           PARTITION BY user_id, name
           ORDER BY created_at ASC, id ASC
         ) AS row_num
  FROM subscription_groups
  WHERE deleted_at IS NULL
)
UPDATE subscriptions s
SET subscription_group_id = ranked.canonical_id
FROM ranked
WHERE ranked.row_num > 1
  AND s.subscription_group_id = ranked.id
  AND s.deleted_at IS NULL;
`).Error; err != nil {
				return fmt.Errorf("reassign duplicate subscription groups: %w", err)
			}
		}
		return db.Exec(`
DELETE FROM subscription_groups sg
USING (
  SELECT ctid
  FROM (
    SELECT ctid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, name
             ORDER BY created_at ASC, id ASC
           ) AS row_num
    FROM subscription_groups
    WHERE deleted_at IS NULL
  ) ranked
  WHERE ranked.row_num > 1
) duplicates
WHERE sg.ctid = duplicates.ctid;
`).Error
	case "sqlite":
		if db.Migrator().HasTable("subscriptions") {
			if err := db.Exec(`
WITH ranked AS (
  SELECT id,
         FIRST_VALUE(id) OVER (
           PARTITION BY user_id, name
           ORDER BY created_at ASC, id ASC
         ) AS canonical_id,
         ROW_NUMBER() OVER (
           PARTITION BY user_id, name
           ORDER BY created_at ASC, id ASC
         ) AS row_num
  FROM subscription_groups
  WHERE deleted_at IS NULL
)
UPDATE subscriptions
SET subscription_group_id = (
  SELECT canonical_id
  FROM ranked
  WHERE ranked.id = subscriptions.subscription_group_id
    AND ranked.row_num > 1
)
WHERE deleted_at IS NULL
  AND subscription_group_id IN (
    SELECT id FROM ranked WHERE row_num > 1
  );
`).Error; err != nil {
				return fmt.Errorf("reassign duplicate subscription groups: %w", err)
			}
		}
		return db.Exec(`
DELETE FROM subscription_groups
WHERE rowid IN (
  SELECT rowid
  FROM (
    SELECT rowid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, name
             ORDER BY created_at ASC, id ASC
           ) AS row_num
    FROM subscription_groups
    WHERE deleted_at IS NULL
  )
  WHERE row_num > 1
);
`).Error
	default:
		return fmt.Errorf("unsupported dialect for subscription group dedupe: %s", db.Dialector.Name())
	}
}
