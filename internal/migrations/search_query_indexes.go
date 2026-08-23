package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

// RunSearchQueryIndexes adds the B-tree indexes used by visibility, ordering,
// pagination, and permission filters. Trigram indexes are kept in the global
// search migration so their heavier write cost remains explicit.
func RunSearchQueryIndexes(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "pgx" {
		return nil
	}
	for _, statement := range searchQueryIndexStatements() {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("create search query index: %w", err)
		}
	}
	return nil
}

func searchQueryIndexStatements() []string {
	return []string{
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_posts_public_search_order
			ON posts (published_at DESC, created_at DESC, id DESC)
			WHERE deleted_at IS NULL AND status = 'published' AND COALESCE(visibility, '') IN ('', 'public')`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_sources_visible_type_language
			ON feed_sources (source_type, language_code, id)
			WHERE deleted_at IS NULL AND hidden = false`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_items_public_published
			ON feed_items (published_at DESC, id DESC)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_items_source_published_live
			ON feed_items (feed_source_id, published_at DESC, id DESC)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_subscriptions_user_source_live
			ON subscriptions (user_id, feed_source_id)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_discussion_targets_kind_resource_live
			ON discussion_targets (kind, resource_id)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_forum_permissions_category_view
			ON forum_category_permissions (category_id, can_view, group_id)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_forum_group_members_user_group
			ON forum_group_members (user_id, group_id)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_debates_status_created_live
			ON debates (status, created_at DESC, id DESC)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_timeline_events_public_date
			ON timeline_events (event_date ASC, id ASC)
			WHERE deleted_at IS NULL AND is_public = true`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_timeline_persons_public_name
			ON timeline_persons (name ASC, id ASC)
			WHERE deleted_at IS NULL AND is_public = true`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_item_stars_item
			ON feed_item_stars (feed_item_id)`,
	}
}
