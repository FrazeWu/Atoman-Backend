package migrations

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

func RunBlogInteractionUniqueIndexes(db *gorm.DB) error {
	if !db.Migrator().HasTable("likes") &&
		!db.Migrator().HasTable("bookmarks") &&
		!db.Migrator().HasTable("video_bookmarks") &&
		!db.Migrator().HasTable("podcast_episode_bookmarks") &&
		!db.Migrator().HasTable("channel_bookmarks") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := DeduplicateBlogInteractions(tx); err != nil {
			return err
		}

		if tx.Migrator().HasTable("likes") {
			if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_likes_user_target
				ON likes (user_id, target_type, target_id)
				WHERE deleted_at IS NULL`).Error; err != nil {
				return err
			}
		}

		if tx.Migrator().HasTable("bookmarks") {
			if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_bookmarks_user_post
				ON bookmarks (user_id, post_id)
				WHERE deleted_at IS NULL`).Error; err != nil {
				return err
			}
		}

		if tx.Migrator().HasTable("video_bookmarks") {
			if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_video_bookmarks_user_video
				ON video_bookmarks (user_id, video_id)
				WHERE deleted_at IS NULL`).Error; err != nil {
				return err
			}
		}

		if tx.Migrator().HasTable("podcast_episode_bookmarks") {
			if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_podcast_episode_bookmarks_user_episode
				ON podcast_episode_bookmarks (user_id, episode_id)
				WHERE deleted_at IS NULL`).Error; err != nil {
				return err
			}
		}

		if tx.Migrator().HasTable("channel_bookmarks") {
			if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_bookmarks_user_channel_kind
				ON channel_bookmarks (user_id, channel_id, kind)
				WHERE deleted_at IS NULL`).Error; err != nil {
				return err
			}
		}

		return nil
	})
}

func DeduplicateBlogInteractions(db *gorm.DB) error {
	if !db.Migrator().HasTable("likes") &&
		!db.Migrator().HasTable("bookmarks") &&
		!db.Migrator().HasTable("video_bookmarks") &&
		!db.Migrator().HasTable("podcast_episode_bookmarks") &&
		!db.Migrator().HasTable("channel_bookmarks") {
		return nil
	}

	switch db.Dialector.Name() {
	case "postgres":
		if db.Migrator().HasTable("likes") {
			if err := db.Exec(`
DELETE FROM likes l
USING (
  SELECT ctid
  FROM (
    SELECT ctid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, target_type, target_id
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM likes
    WHERE deleted_at IS NULL
  ) ranked
  WHERE ranked.row_num > 1
) duplicates
WHERE l.ctid = duplicates.ctid;
`).Error; err != nil {
				return fmt.Errorf("deduplicate likes: %w", err)
			}
		}

		if db.Migrator().HasTable("bookmarks") {
			if err := db.Exec(`
DELETE FROM bookmarks b
USING (
  SELECT ctid
  FROM (
    SELECT ctid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, post_id
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM bookmarks
    WHERE deleted_at IS NULL
  ) ranked
  WHERE ranked.row_num > 1
) duplicates
WHERE b.ctid = duplicates.ctid;
`).Error; err != nil {
				return fmt.Errorf("deduplicate bookmarks: %w", err)
			}
		}
		if err := deduplicateSimpleBookmarkTablePostgres(db, "video_bookmarks", "video_id"); err != nil {
			return fmt.Errorf("deduplicate video bookmarks: %w", err)
		}
		if err := deduplicateSimpleBookmarkTablePostgres(db, "podcast_episode_bookmarks", "episode_id"); err != nil {
			return fmt.Errorf("deduplicate podcast episode bookmarks: %w", err)
		}
		if err := deduplicateChannelBookmarksPostgres(db); err != nil {
			return fmt.Errorf("deduplicate channel bookmarks: %w", err)
		}
		return nil
	case "sqlite":
		if db.Migrator().HasTable("likes") {
			if err := db.Exec(`
DELETE FROM likes
WHERE rowid IN (
  SELECT rowid
  FROM (
    SELECT rowid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, target_type, target_id
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM likes
    WHERE deleted_at IS NULL
  )
  WHERE row_num > 1
);
`).Error; err != nil {
				return fmt.Errorf("deduplicate likes: %w", err)
			}
		}

		if db.Migrator().HasTable("bookmarks") {
			if err := db.Exec(`
DELETE FROM bookmarks
WHERE rowid IN (
  SELECT rowid
  FROM (
    SELECT rowid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, post_id
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM bookmarks
    WHERE deleted_at IS NULL
  )
  WHERE row_num > 1
);
`).Error; err != nil {
				return fmt.Errorf("deduplicate bookmarks: %w", err)
			}
		}
		if err := deduplicateSimpleBookmarkTableSQLite(db, "video_bookmarks", "video_id"); err != nil {
			return fmt.Errorf("deduplicate video bookmarks: %w", err)
		}
		if err := deduplicateSimpleBookmarkTableSQLite(db, "podcast_episode_bookmarks", "episode_id"); err != nil {
			return fmt.Errorf("deduplicate podcast episode bookmarks: %w", err)
		}
		if err := deduplicateChannelBookmarksSQLite(db); err != nil {
			return fmt.Errorf("deduplicate channel bookmarks: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported dialect for blog interaction dedupe: %s", db.Dialector.Name())
	}
}

func deduplicateSimpleBookmarkTablePostgres(db *gorm.DB, table string, column string) error {
	if !db.Migrator().HasTable(table) {
		return nil
	}
	return db.Exec(fmt.Sprintf(`
DELETE FROM %[1]s t
USING (
  SELECT ctid
  FROM (
    SELECT ctid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, %[2]s
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM %[1]s
    WHERE deleted_at IS NULL
  ) ranked
  WHERE ranked.row_num > 1
) duplicates
WHERE t.ctid = duplicates.ctid;
`, table, column)).Error
}

func deduplicateSimpleBookmarkTableSQLite(db *gorm.DB, table string, column string) error {
	if !db.Migrator().HasTable(table) {
		return nil
	}
	return db.Exec(fmt.Sprintf(`
DELETE FROM %[1]s
WHERE rowid IN (
  SELECT rowid
  FROM (
    SELECT rowid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, %[2]s
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM %[1]s
    WHERE deleted_at IS NULL
  )
  WHERE row_num > 1
);
`, table, column)).Error
}

func deduplicateChannelBookmarksPostgres(db *gorm.DB) error {
	if !db.Migrator().HasTable("channel_bookmarks") {
		return nil
	}
	return db.Exec(`
DELETE FROM channel_bookmarks t
USING (
  SELECT ctid
  FROM (
    SELECT ctid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, channel_id, kind
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM channel_bookmarks
    WHERE deleted_at IS NULL
  ) ranked
  WHERE ranked.row_num > 1
) duplicates
WHERE t.ctid = duplicates.ctid;
`).Error
}

func deduplicateChannelBookmarksSQLite(db *gorm.DB) error {
	if !db.Migrator().HasTable("channel_bookmarks") {
		return nil
	}
	return db.Exec(`
DELETE FROM channel_bookmarks
WHERE rowid IN (
  SELECT rowid
  FROM (
    SELECT rowid,
           ROW_NUMBER() OVER (
             PARTITION BY user_id, channel_id, kind
             ORDER BY created_at DESC, id DESC
           ) AS row_num
    FROM channel_bookmarks
    WHERE deleted_at IS NULL
  )
  WHERE row_num > 1
);
`).Error
}

func IsBlogInteractionDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		constraint := strings.ToLower(pgErr.ConstraintName)
		detail := strings.ToLower(pgErr.Detail)
		return constraint == "idx_likes_user_target" ||
			constraint == "idx_bookmarks_user_post" ||
			constraint == "idx_video_bookmarks_user_video" ||
			constraint == "idx_podcast_episode_bookmarks_user_episode" ||
			constraint == "idx_channel_bookmarks_user_channel_kind" ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "target_type") && strings.Contains(detail, "target_id")) ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "post_id")) ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "video_id")) ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "episode_id")) ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "channel_id") && strings.Contains(detail, "kind"))
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == "23505" {
		constraint := strings.ToLower(pqErr.Constraint)
		detail := strings.ToLower(pqErr.Detail)
		return constraint == "idx_likes_user_target" ||
			constraint == "idx_bookmarks_user_post" ||
			constraint == "idx_video_bookmarks_user_video" ||
			constraint == "idx_podcast_episode_bookmarks_user_episode" ||
			constraint == "idx_channel_bookmarks_user_channel_kind" ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "target_type") && strings.Contains(detail, "target_id")) ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "post_id")) ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "video_id")) ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "episode_id")) ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "channel_id") && strings.Contains(detail, "kind"))
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "idx_likes_user_target") ||
		strings.Contains(message, "idx_bookmarks_user_post") ||
		strings.Contains(message, "idx_video_bookmarks_user_video") ||
		strings.Contains(message, "idx_podcast_episode_bookmarks_user_episode") ||
		strings.Contains(message, "idx_channel_bookmarks_user_channel_kind") ||
		(strings.Contains(message, "unique constraint failed") && strings.Contains(message, "likes.user_id")) ||
		(strings.Contains(message, "unique constraint failed") && strings.Contains(message, "bookmarks.user_id")) ||
		(strings.Contains(message, "unique constraint failed") && strings.Contains(message, "video_bookmarks.user_id")) ||
		(strings.Contains(message, "unique constraint failed") && strings.Contains(message, "podcast_episode_bookmarks.user_id")) ||
		(strings.Contains(message, "unique constraint failed") && strings.Contains(message, "channel_bookmarks.user_id") && strings.Contains(message, "channel_bookmarks.kind")) ||
		(strings.Contains(message, "duplicate key") && strings.Contains(message, "likes")) ||
		(strings.Contains(message, "duplicate key") && strings.Contains(message, "bookmarks")) ||
		(strings.Contains(message, "duplicate key") && strings.Contains(message, "video_bookmarks")) ||
		(strings.Contains(message, "duplicate key") && strings.Contains(message, "podcast_episode_bookmarks")) ||
		(strings.Contains(message, "duplicate key") && strings.Contains(message, "channel_bookmarks"))
}
