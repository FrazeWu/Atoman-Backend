package feed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/gorm"
)

// snapshotCopyExperimentEnabled fails closed unless the benchmark explicitly
// opted in AND the GORM handle is connected to a disposable test schema in a
// dedicated _test database. No production setting can enable COPY accidentally
// in the public feed publisher by setting only one flag.
func snapshotCopyExperimentEnabled(ctx context.Context, db *gorm.DB) (bool, error) {
	if os.Getenv("FEED_SCALE_ISOLATED") != "yes" || os.Getenv("FEED_SNAPSHOT_COPY_EXPERIMENT") != "yes" {
		return false, nil
	}
	var target struct {
		Database string `gorm:"column:database"`
		Schema   string `gorm:"column:schema"`
	}
	if err := db.WithContext(ctx).Raw(`SELECT current_database() AS database, current_schema() AS schema`).Scan(&target).Error; err != nil {
		return false, fmt.Errorf("COPY experiment cannot verify isolation: %w", err)
	}
	if !snapshotCopyTargetAllowed(target.Database, target.Schema) {
		return false, fmt.Errorf("COPY experiment refused outside disposable test schema in _test database")
	}
	return true, nil
}

func snapshotCopyTargetAllowed(database, schema string) bool {
	// testdb.Open generates only test_<32 hex> schemas. Refuse any
	// similarly-named ordinary application schema even in a *_test DB.
	if database != "test" && !strings.HasPrefix(database, "test_") && !strings.HasSuffix(database, "_test") {
		return false
	}
	if len(schema) != 37 || !strings.HasPrefix(schema, "test_") {
		return false
	}
	_, err := uuid.Parse(schema[len("test_"):])
	return err == nil
}

// publishDedupSnapshotViaCopyExperiment is not enabled by any production route.
// Its pgx transaction includes generation metadata, both COPY streams and the
// live scope-head swap. An error or cancellation rolls back ALL changes.
func (r *Repo) publishDedupSnapshotViaCopyExperiment(ctx context.Context, generation uuid.UUID,
	rows []feedDedupSnapshotRow, members []feedDedupSnapshotMember,
	head feedDedupSnapshotHead, stage func(string, time.Time)) error {

	sqlDB, err := r.db.DB()
	if err != nil {
		return fmt.Errorf("COPY experiment needs sql DB: %w", err)
	}
	connection, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	fault := os.Getenv("FEED_SNAPSHOT_COPY_FAIL_AT")

	return connection.Raw(func(raw any) error {
		driverConn, ok := raw.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("COPY experiment requires pgx stdlib, got %T", raw)
		}
		tx, err := driverConn.Conn().Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		fail := func(stage string) error {
			if fault == stage {
				return fmt.Errorf("injected COPY snapshot failure at %s", stage)
			}
			return nil
		}
		if err := fail("before_generation"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO feed_dedup_snapshot_generations (generation_id,scope,total,built_at)
VALUES ($1,$2,$3,$4)`, generation, publicRecentDedupScope, head.Total, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := fail("before_rows"); err != nil {
			return err
		}

		beginRows := time.Now()
		copied, err := tx.CopyFrom(ctx, pgx.Identifier{"feed_dedup_snapshot_rows"}, []string{
			"generation_id", "position", "feed_item_id", "published_at", "normalized_url", "normalized_title", "duplicate_count", "duplicate_member_ids", "duplicate_sources",
		}, pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			if fault == "during_rows" && i == len(rows)/2 {
				return nil, errors.New("injected COPY snapshot failure during_rows")
			}
			x := rows[i]
			return []any{x.GenerationID, x.Position, x.FeedItemID, x.PublishedAt, x.NormalizedURL, x.NormalizedTitle, x.DuplicateCount, string(x.DuplicateMemberIDs), string(x.DuplicateSources)}, nil
		}))
		stage("insert_representatives", beginRows)
		if err != nil {
			return err
		}
		if copied != int64(len(rows)) {
			return fmt.Errorf("COPY rows inserted %d want %d", copied, len(rows))
		}
		if err := fail("after_rows"); err != nil {
			return err
		}

		beginMembers := time.Now()
		copied, err = tx.CopyFrom(ctx, pgx.Identifier{"feed_dedup_snapshot_members"}, []string{
			"generation_id", "feed_item_id", "published_at", "normalized_url", "normalized_url_hash", "normalized_title",
		}, pgx.CopyFromSlice(len(members), func(i int) ([]any, error) {
			if fault == "during_members" && i == len(members)/2 {
				return nil, errors.New("injected COPY snapshot failure during_members")
			}
			x := members[i]
			return []any{x.GenerationID, x.FeedItemID, x.PublishedAt, x.NormalizedURL, x.NormalizedURLHash, x.NormalizedTitle}, nil
		}))
		stage("insert_members", beginMembers)
		if err != nil {
			return err
		}
		if copied != int64(len(members)) {
			return fmt.Errorf("COPY members inserted %d want %d", copied, len(members))
		}
		if err := fail("after_members"); err != nil {
			return err
		}
		if err := fail("before_head"); err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `INSERT INTO feed_dedup_snapshot_heads
(scope,generation_id,revision,total,built_at,source_versions,tail_published_at,tail_feed_item_id,first_published_at,first_feed_item_id,position_base,raw_item_count)
VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,$11,$12)
ON CONFLICT (scope) DO UPDATE SET
 generation_id=EXCLUDED.generation_id,revision=EXCLUDED.revision,total=EXCLUDED.total,
 built_at=EXCLUDED.built_at,source_versions=EXCLUDED.source_versions,
 tail_published_at=EXCLUDED.tail_published_at,tail_feed_item_id=EXCLUDED.tail_feed_item_id,
 first_published_at=EXCLUDED.first_published_at,first_feed_item_id=EXCLUDED.first_feed_item_id,
 position_base=EXCLUDED.position_base,raw_item_count=EXCLUDED.raw_item_count`,
			head.Scope, head.GenerationID, head.Revision, head.Total, head.BuiltAt, string(head.SourceVersions),
			head.TailPublishedAt, head.TailFeedItemID, head.FirstPublishedAt, head.FirstFeedItemID, head.PositionBase, head.RawItemCount)
		if err != nil {
			return err
		}
		if err := fail("after_head"); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}
