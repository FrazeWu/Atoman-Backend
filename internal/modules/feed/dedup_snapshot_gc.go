package feed

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// pruneInactiveDedupSnapshotGenerations is deliberately not scheduled.
// A production worker must establish a max-reader lifetime and an offline
// maintenance policy before enabling retention. The grace period ensures
// in-flight readers on a prior generation can finish without data loss.
func (r *Repo) pruneInactiveDedupSnapshotGenerations(ctx context.Context, grace time.Duration) (int64, error) {
	if grace < time.Hour {
		return 0, fmt.Errorf("snapshot cleanup grace must be >= 1h")
	}
	cutoff := time.Now().UTC().Add(-grace)
	var removed int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var obsolete []feedDedupSnapshotGeneration
		err := tx.Model(&feedDedupSnapshotGeneration{}).
			Where("scope = ? AND built_at < ?", publicRecentDedupScope, cutoff).
			Where("generation_id NOT IN (SELECT generation_id FROM feed_dedup_snapshot_heads WHERE scope = ?)", publicRecentDedupScope).
			Find(&obsolete).Error
		if err != nil {
			return err
		}
		for _, generation := range obsolete {
			if err := tx.Where("generation_id = ?", generation.GenerationID).Delete(&feedDedupSnapshotMember{}).Error; err != nil {
				return err
			}
			if err := tx.Where("generation_id = ?", generation.GenerationID).Delete(&feedDedupSnapshotRow{}).Error; err != nil {
				return err
			}
			if err := tx.Where("generation_id = ?", generation.GenerationID).Delete(&feedDedupSnapshotGeneration{}).Error; err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	return removed, err
}
