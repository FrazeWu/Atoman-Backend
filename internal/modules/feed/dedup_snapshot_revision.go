package feed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// One transactional revision per feed source avoids a global write lock. The
// ledger is append-only with respect to source IDs: removing a source does not
// remove its revision, so the aggregate remains strictly increasing.
type feedDedupSourceRevision struct {
	FeedSourceID uuid.UUID `gorm:"type:uuid;primaryKey"`
	Revision     int64     `gorm:"not null;default:0"`
	UpdatedAt    time.Time `gorm:"not null"`
}

func (feedDedupSourceRevision) TableName() string { return "feed_dedup_source_revisions" }

// An append-only statement-level change journal makes it possible to discover
// exactly which sources and item IDs changed since a published generation.
// Journals are NOT pruned until a checkpoint/watermark retention policy exists.
type feedDedupChangeEvent struct {
	FeedSourceID uuid.UUID       `gorm:"type:uuid;primaryKey"`
	Revision     int64           `gorm:"primaryKey"`
	Kind         string          `gorm:"not null;size:40"`
	ItemIDs      json.RawMessage `gorm:"type:jsonb;not null"`
	CreatedAt    time.Time       `gorm:"not null"`
}

func (feedDedupChangeEvent) TableName() string { return "feed_dedup_change_events" }

func (r *Repo) dedupSourceEventsAfter(ctx context.Context, sourceID uuid.UUID, revision int64) ([]feedDedupChangeEvent, error) {
	var events []feedDedupChangeEvent
	err := r.db.WithContext(ctx).Where("feed_source_id = ? AND revision > ?", sourceID, revision).
		Order("revision ASC").Find(&events).Error
	return events, err
}

// installDedupRevisionTracking is a candidate migration and must run only in
// an isolated test database until the rollout has been reviewed. It installs
// eight statement-level triggers (INSERT/UPDATE/DELETE plus TRUNCATE
// on feed_items and feed_sources). Each statement increments each affected
// source revision only once, rather than once per item.
func installDedupRevisionTracking(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var schema string
		if err := tx.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
			return err
		}
		if !strings.HasPrefix(schema, "test_") {
			return fmt.Errorf("refusing experimental feed revision triggers outside an isolated test_ schema")
		}
		if err := tx.AutoMigrate(&feedDedupSourceRevision{}, &feedDedupChangeEvent{}); err != nil {
			return err
		}
		quote := func(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
		prefix := quote(schema) + "."
		revisionTable := prefix + quote("feed_dedup_source_revisions")
		changeTable := prefix + quote("feed_dedup_change_events")
		bump := func(sourceIDs string, kind string) string {
			return fmt.Sprintf(`WITH affected AS (
				SELECT source_id, COALESCE(jsonb_agg(DISTINCT item_id) FILTER (WHERE item_id IS NOT NULL), '[]'::jsonb) AS item_ids
				FROM (%s) AS changed WHERE source_id IS NOT NULL GROUP BY source_id
			), bumped AS (
				INSERT INTO %s AS ledger (feed_source_id, revision, updated_at)
				SELECT source_id, 1, clock_timestamp() FROM affected
				ON CONFLICT (feed_source_id) DO UPDATE
					SET revision = ledger.revision + 1, updated_at = clock_timestamp()
				RETURNING feed_source_id, revision
			)
			INSERT INTO %s (feed_source_id, revision, kind, item_ids, created_at)
			SELECT b.feed_source_id, b.revision, '%s', a.item_ids, clock_timestamp()
			FROM bumped b JOIN affected a ON a.source_id = b.feed_source_id;`, sourceIDs, revisionTable, changeTable, kind)
		}

		itemChanged := `(
          n.title, n.link, n.author, n.published_at, n.feed_source_id, n.deleted_at
        ) IS DISTINCT FROM (
          o.title, o.link, o.author, o.published_at, o.feed_source_id, o.deleted_at
        )`
		sourceChanged := `(
          n.hidden, n.deleted_at, n.title, n.source_type, n.rss_url, n.canonical_url,
          n.site_url, n.category, n.language_code
        ) IS DISTINCT FROM (
          o.hidden, o.deleted_at, o.title, o.source_type, o.rss_url, o.canonical_url,
          o.site_url, o.category, o.language_code
        )`
		branches := map[string]string{
			"feed_items_insert": bump("SELECT feed_source_id AS source_id, id AS item_id FROM new_rows", "feed_items_insert"),
			"feed_items_delete": bump("SELECT feed_source_id AS source_id, id AS item_id FROM old_rows", "feed_items_delete"),
			"feed_items_update": bump(fmt.Sprintf(`
				SELECT n.feed_source_id AS source_id, n.id AS item_id FROM new_rows n JOIN old_rows o USING(id) WHERE %s
				UNION ALL
				SELECT o.feed_source_id AS source_id, o.id AS item_id FROM new_rows n JOIN old_rows o USING(id)
				WHERE %s AND o.feed_source_id IS DISTINCT FROM n.feed_source_id`, itemChanged, itemChanged), "feed_items_update"),
			"feed_sources_insert": bump("SELECT id AS source_id, NULL::uuid AS item_id FROM new_rows", "feed_sources_insert"),
			"feed_sources_delete": bump("SELECT id AS source_id, NULL::uuid AS item_id FROM old_rows", "feed_sources_delete"),
			"feed_sources_update": bump(fmt.Sprintf(`SELECT n.id AS source_id, NULL::uuid AS item_id FROM new_rows n JOIN old_rows o USING(id) WHERE %s`, sourceChanged), "feed_sources_update"),
			// TRUNCATE does not fire DELETE triggers. A durable sentinel revision
			// invalidates every generation when an operator truncates a table.
			"feed_items_truncate":   bump("SELECT '00000000-0000-0000-0000-000000000000'::uuid AS source_id, NULL::uuid AS item_id", "feed_items_truncate"),
			"feed_sources_truncate": bump("SELECT '00000000-0000-0000-0000-000000000000'::uuid AS source_id, NULL::uuid AS item_id", "feed_sources_truncate"),
		}

		// All SQL is assembled from fixed table/column names and the quoted
		// current_schema(), never user-provided names or expressions.
		for _, entry := range []struct{ table, operation, transition string }{
			{"feed_items", "insert", "REFERENCING NEW TABLE AS new_rows"},
			{"feed_items", "delete", "REFERENCING OLD TABLE AS old_rows"},
			{"feed_items", "update", "REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows"},
			{"feed_sources", "insert", "REFERENCING NEW TABLE AS new_rows"},
			{"feed_sources", "delete", "REFERENCING OLD TABLE AS old_rows"},
			{"feed_sources", "update", "REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows"},
			{"feed_items", "truncate", ""},
			{"feed_sources", "truncate", ""},
		} {
			key := entry.table + "_" + entry.operation
			fn := prefix + quote("feed_dedup_revision_"+key)
			ddl := fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS $body$
            BEGIN
               %s
               RETURN NULL;
            END;
            $body$ LANGUAGE plpgsql;`, fn, branches[key])
			if err := tx.Exec(ddl).Error; err != nil {
				return fmt.Errorf("install revision function %s: %w", key, err)
			}
			triggerName := quote("feed_dedup_rev_" + key)
			tableName := prefix + quote(entry.table)
			if err := tx.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s", triggerName, tableName)).Error; err != nil {
				return err
			}
			ddl = fmt.Sprintf("CREATE TRIGGER %s AFTER %s ON %s %s FOR EACH STATEMENT EXECUTE FUNCTION %s()", triggerName, strings.ToUpper(entry.operation), tableName, entry.transition, fn)
			if err := tx.Exec(ddl).Error; err != nil {
				return fmt.Errorf("install revision trigger %s: %w", key, err)
			}
		}
		return nil
	})
}

// In a single PostgreSQL statement, SUM(bigint) returns numeric and is exact.
// No revision row is ever deleted by this module, so every committed relevant
// change strictly increases the aggregate. There is no global lock or counter
// row that all RSS workers must update. The scan is O(number of sources).
func (r *Repo) dedupSourceRevision(ctx context.Context) (string, error) {
	var revision string
	err := r.db.WithContext(ctx).Raw(`SELECT COALESCE(SUM(revision), 0)::text FROM feed_dedup_source_revisions`).Scan(&revision).Error
	if err != nil {
		return "", err
	}
	if revision == "" {
		return strconv.FormatInt(0, 10), nil
	}
	return revision, nil
}

// The rebuild is not yet scheduled in production. The read path verifies the
// source aggregate in a repeatable-read transaction to fail closed on changes.
func (r *Repo) publishPublicRecentDedupSnapshotTracked(ctx context.Context) (int64, error) {
	before, err := r.dedupSourceRevision(ctx)
	if err != nil {
		return 0, err
	}
	total, err := r.publishPublicRecentDedupSnapshot(ctx, before)
	if err != nil {
		return 0, err
	}
	after, err := r.dedupSourceRevision(ctx)
	if err != nil {
		return 0, err
	}
	if after != before {
		return 0, ErrDedupSnapshotStale
	}
	return total, nil
}

func (r *Repo) readPublicRecentDedupSnapshotPageTracked(ctx context.Context, page, pageSize int) ([]model.FeedItem, int64, error) {
	tx := r.db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer tx.Rollback()
	reader := &Repo{db: tx}
	revision, err := reader.dedupSourceRevision(ctx)
	if err != nil {
		return nil, 0, err
	}
	items, total, err := reader.readPublicRecentDedupSnapshotPage(ctx, revision, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}
