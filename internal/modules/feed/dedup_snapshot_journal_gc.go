package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// pruneDedupChangeEventsBeforeCheckpoint is opt-in maintenance, not scheduled.
// Only changes at or below the active head's per-source checkpoint are eligible
// for retention-based deletion; more recent revisions must stay replayable.
// Locking the head prevents checkpoint changes while pruning. When there is no
// valid active checkpoint, keep the entire journal.
func (r *Repo) pruneDedupChangeEventsBeforeCheckpoint(ctx context.Context, grace time.Duration) (int64, error) {
	if grace < 24*time.Hour {
		return 0, fmt.Errorf("dedup event retention grace must be >= 24h")
	}
	cutoff := time.Now().UTC().Add(-grace)
	var removed int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var head feedDedupSnapshotHead
		err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
			Where("scope = ?", publicRecentDedupScope).Take(&head).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var versions map[string]int64
		if err := json.Unmarshal(head.SourceVersions, &versions); err != nil {
			return err
		}
		if len(versions) == 0 {
			return nil
		}
		result := tx.Exec(`DELETE FROM feed_dedup_change_events AS event
            USING jsonb_each_text(CAST(? AS jsonb)) AS checkpoint(source_id, revision)
            WHERE event.feed_source_id = CAST(checkpoint.source_id AS uuid)
              AND event.revision <= CAST(checkpoint.revision AS bigint)
              AND event.created_at < ?`, string(head.SourceVersions), cutoff)
		if result.Error != nil {
			return result.Error
		}
		removed = result.RowsAffected
		return nil
	})
	return removed, err
}
