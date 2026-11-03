package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"atoman/internal/model"
	legacyfeed "atoman/internal/service"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This is an opt-in database-backed deduplication projection. It is NOT
// registered as a migration, activated in HTTP routes, or used to refresh
// production data. Callers must obtain a reliable source revision before
// activating the read model: a stale projection must never silently serve as
// if it were live data.
const publicRecentDedupScope = "public_recent_v1"

var ErrDedupSnapshotStale = errors.New("feed deduplication snapshot unavailable or stale")

type feedDedupSnapshotHead struct {
	Scope        string    `gorm:"primaryKey;size:96"`
	GenerationID uuid.UUID `gorm:"type:uuid;not null"`
	Revision     string    `gorm:"not null;size:180"`
	Total        int64     `gorm:"not null"`
	BuiltAt      time.Time `gorm:"not null"`
}

func (feedDedupSnapshotHead) TableName() string { return "feed_dedup_snapshot_heads" }

// Each committed generation gets its own creation record so stale generations
// can be collected after a grace period, without removing the live pointer.
type feedDedupSnapshotGeneration struct {
	GenerationID uuid.UUID `gorm:"type:uuid;primaryKey"`
	Scope        string    `gorm:"not null;size:96"`
	Total        int64     `gorm:"not null"`
	BuiltAt      time.Time `gorm:"not null"`
}

func (feedDedupSnapshotGeneration) TableName() string { return "feed_dedup_snapshot_generations" }

type feedDedupSnapshotRow struct {
	GenerationID       uuid.UUID       `gorm:"type:uuid;primaryKey"`
	Position           int64           `gorm:"primaryKey"`
	FeedItemID         uuid.UUID       `gorm:"type:uuid;not null"`
	PublishedAt        time.Time       `gorm:"not null"`
	DuplicateCount     int             `gorm:"not null"`
	DuplicateMemberIDs json.RawMessage `gorm:"type:jsonb;not null"`
	DuplicateSources   json.RawMessage `gorm:"type:jsonb;not null"`
}

func (feedDedupSnapshotRow) TableName() string { return "feed_dedup_snapshot_rows" }

// publishPublicRecentDedupSnapshot rebuilds the exact greedy clusters from
// the same narrow, newest-first rows as the legacy public feed path. The row
// inserts and the live-generation pointer are committed atomically. Old
// generations are retained for readers that raced a generation swap.
// This currently rebuilds from scratch; incremental maintenance is pending.
func (r *Repo) publishPublicRecentDedupSnapshot(ctx context.Context, revision string) (int64, error) {
	if strings.TrimSpace(revision) == "" {
		return 0, fmt.Errorf("feed dedup snapshot requires an upstream revision")
	}
	var items []model.FeedItem
	err := r.db.WithContext(ctx).Model(&model.FeedItem{}).
		Select(feedDuplicateScanColumns).
		Preload("FeedSource").
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_sources.hidden = ? AND feed_sources.deleted_at IS NULL", false).
		Order("feed_items.published_at DESC, feed_items.id DESC").
		Find(&items).Error
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	legacyfeed.AnnotateDuplicateFeedItems(items)
	generationID := uuid.New()
	rows := make([]feedDedupSnapshotRow, 0, len(items))
	for _, item := range items {
		if item.IsDuplicate {
			continue
		}
		memberIDs, err := json.Marshal(item.DuplicateItemIDs)
		if err != nil {
			return 0, err
		}
		sources, err := json.Marshal(item.DuplicateSources)
		if err != nil {
			return 0, err
		}
		rows = append(rows, feedDedupSnapshotRow{
			GenerationID: generationID, Position: int64(len(rows)), FeedItemID: item.ID,
			PublishedAt: item.PublishedAt, DuplicateCount: item.DuplicateCount,
			DuplicateMemberIDs: memberIDs, DuplicateSources: sources,
		})
	}
	total := int64(len(rows))
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		generation := feedDedupSnapshotGeneration{GenerationID: generationID, Scope: publicRecentDedupScope, Total: total, BuiltAt: time.Now().UTC()}
		if err := tx.Create(&generation).Error; err != nil {
			return err
		}
		// Chunking keeps each INSERT below PostgreSQL parameter limits.
		for start := 0; start < len(rows); start += 350 {
			end := start + 350
			if end > len(rows) {
				end = len(rows)
			}
			if err := tx.Create(rows[start:end]).Error; err != nil {
				return err
			}
		}
		head := feedDedupSnapshotHead{
			Scope: publicRecentDedupScope, GenerationID: generationID,
			Revision: revision, Total: total, BuiltAt: time.Now().UTC(),
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "scope"}},
			DoUpdates: clause.AssignmentColumns([]string{"generation_id", "revision", "total", "built_at"}),
		}).Create(&head).Error
	})
	return total, err
}

// readPublicRecentDedupSnapshotPage seeks directly into a stable ordinal in
// PostgreSQL (generation_id, position) rather than scanning OFFSET preceding
// rows. A caller must pass the current source revision; mismatches are errors,
// never silently returned stale pages.
func (r *Repo) readPublicRecentDedupSnapshotPage(ctx context.Context, revision string, page, pageSize int) ([]model.FeedItem, int64, error) {
	if strings.TrimSpace(revision) == "" {
		return nil, 0, ErrDedupSnapshotStale
	}
	var head feedDedupSnapshotHead
	err := r.db.WithContext(ctx).Where("scope = ?", publicRecentDedupScope).First(&head).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, 0, ErrDedupSnapshotStale
	}
	if err != nil {
		return nil, 0, err
	}
	if head.Revision != revision {
		return nil, 0, ErrDedupSnapshotStale
	}
	page = normalizedPage(page)
	pageSize = normalizedPageSize(pageSize)
	if int64(page-1) > math.MaxInt64/int64(pageSize) {
		return []model.FeedItem{}, head.Total, nil
	}
	start := int64(page-1) * int64(pageSize)
	if start >= head.Total {
		return []model.FeedItem{}, head.Total, nil
	}

	var projectionRows []feedDedupSnapshotRow
	if err := r.db.WithContext(ctx).Where("generation_id = ? AND position >= ?", head.GenerationID, start).
		Order("position ASC").Limit(pageSize).Find(&projectionRows).Error; err != nil {
		return nil, 0, err
	}
	ids := make([]uuid.UUID, 0, len(projectionRows))
	for _, row := range projectionRows {
		ids = append(ids, row.FeedItemID)
	}
	full := make(map[uuid.UUID]model.FeedItem, len(ids))
	if len(ids) > 0 {
		var hydrated []model.FeedItem
		if err := r.db.WithContext(ctx).Preload("FeedSource").Where("id IN ?", ids).Find(&hydrated).Error; err != nil {
			return nil, 0, err
		}
		for _, item := range hydrated {
			full[item.ID] = item
		}
	}
	items := make([]model.FeedItem, 0, len(projectionRows))
	for _, row := range projectionRows {
		item, ok := full[row.FeedItemID]
		if !ok {
			return nil, 0, ErrDedupSnapshotStale
		}
		item.DuplicateCount = row.DuplicateCount
		if err := json.Unmarshal(row.DuplicateMemberIDs, &item.DuplicateItemIDs); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(row.DuplicateSources, &item.DuplicateSources); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, head.Total, nil
}
