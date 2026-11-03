package feed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"atoman/internal/model"
	"gorm.io/gorm"
)

type feedDedupRevision struct {
	Scope    string `gorm:"primaryKey;size:96"`
	Revision int64  `gorm:"not null;default:0"`
}

func (feedDedupRevision) TableName() string { return "feed_dedup_revisions" }

// installDedupRevisionTracking is a migration candidate used only by tests at
// this stage. The production rollout requires its own concurrency/lock-load
// review because the revision row is transactional and shared by all writes.
func installDedupRevisionTracking(db *gorm.DB) error {
	if err := db.AutoMigrate(&feedDedupRevision{}); err != nil {
		return err
	}
	if err := db.FirstOrCreate(&feedDedupRevision{}, feedDedupRevision{Scope: publicRecentDedupScope}).Error; err != nil {
		return err
	}
	var schema string
	if err := db.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		return err
	}
	if schema == "" {
		return fmt.Errorf("cannot resolve feed dedup tracking schema")
	}
	quote := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	qualified := quote(schema)
	function := qualified + `."feed_dedup_mark_change"`
	table := qualified + `."feed_dedup_revisions"`
	ddl := fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS $$
BEGIN
    UPDATE %s SET revision = revision + 1 WHERE scope = '%s';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql`, function, table, publicRecentDedupScope)
	if err := db.Exec(ddl).Error; err != nil {
		return err
	}
	for _, entry := range []struct{ table, name, events string }{
		{"feed_items", "feed_dedup_revision_on_items", "INSERT OR DELETE OR UPDATE OF title, link, author, published_at, feed_source_id, deleted_at"},
		{"feed_sources", "feed_dedup_revision_on_sources", "INSERT OR DELETE OR UPDATE OF hidden, deleted_at, title, source_type, rss_url, canonical_url, site_url, category, language_code"},
	} {
		target := qualified + `.` + quote(entry.table)
		trigger := quote(entry.name)
		if err := db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s", trigger, target)).Error; err != nil {
			return err
		}
		if err := db.Exec(fmt.Sprintf("CREATE TRIGGER %s AFTER %s ON %s FOR EACH ROW EXECUTE FUNCTION %s()", trigger, entry.events, target, function)).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) dedupSourceRevision(ctx context.Context) (string, error) {
	var value feedDedupRevision
	err := r.db.WithContext(ctx).Where("scope = ?", publicRecentDedupScope).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrDedupSnapshotStale
	}
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(value.Revision, 10), nil
}

// publishPublicRecentDedupSnapshotTracked never accepts a revision supplied by
// a client. It gets the revision from the database and rechecks after the
// atomic generation swap. If any relevant mutation raced a rebuild, the next
// reader detects the mismatch and uses the legacy path until it is rebuilt.
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

// readPublicRecentDedupSnapshotPageTracked reads the source revision, snapshot
// pointer, projected page and hydrated data in one REPEATABLE READ transaction.
// It therefore never mixes two committed revisions in the same response.
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
