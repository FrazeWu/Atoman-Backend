package migrations

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// RunSearchQueryIndexes adds the B-tree indexes used by visibility, ordering,
// pagination, and permission filters. Trigram indexes are kept in the global
// search migration so their heavier write cost remains explicit.
func RunSearchQueryIndexes(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "pgx" {
		return nil
	}
	availableColumns := searchQuerySchemaColumns(db)
	for _, statement := range searchQueryIndexStatements() {
		if !searchQueryIndexAvailable(statement, availableColumns) {
			continue
		}
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("create search query index: %w", err)
		}
	}
	return nil
}

func searchQuerySchemaColumns(db *gorm.DB) map[string]map[string]struct{} {
	columnsByTable := make(map[string]map[string]struct{})
	for _, requirement := range searchQueryIndexRequirements {
		if _, checked := columnsByTable[requirement.table]; checked {
			continue
		}
		if !searchQuerySchemaTableExists(db, requirement.table) {
			columnsByTable[requirement.table] = nil
			continue
		}
		columnTypes, err := db.Migrator().ColumnTypes(requirement.table)
		if err != nil {
			columnsByTable[requirement.table] = nil
			continue
		}
		columns := make(map[string]struct{}, len(columnTypes))
		for _, columnType := range columnTypes {
			columns[strings.ToLower(columnType.Name())] = struct{}{}
		}
		columnsByTable[requirement.table] = columns
	}
	return columnsByTable
}

type searchQueryIndexRequirement struct {
	name    string
	table   string
	columns []string
}

var searchQueryIndexRequirements = []searchQueryIndexRequirement{
	{name: "idx_posts_public_search_order", table: "posts", columns: []string{"published_at", "created_at", "id", "deleted_at", "status", "visibility"}},
	{name: "idx_feed_sources_visible_type_language", table: "feed_sources", columns: []string{"source_type", "language_code", "id", "deleted_at", "hidden"}},
	{name: "idx_feed_items_public_published", table: "feed_items", columns: []string{"published_at", "id", "deleted_at"}},
	{name: "idx_feed_items_source_published_live", table: "feed_items", columns: []string{"feed_source_id", "published_at", "id", "deleted_at"}},
	{name: "idx_subscriptions_user_source_live", table: "subscriptions", columns: []string{"user_id", "feed_source_id", "deleted_at"}},
	{name: "idx_discussion_targets_kind_resource_live", table: "discussion_targets", columns: []string{"kind", "resource_id", "deleted_at"}},
	{name: "idx_forum_permissions_category_view", table: "forum_category_permissions", columns: []string{"category_id", "can_view", "group_id", "deleted_at"}},
	{name: "idx_forum_group_members_user_group", table: "forum_group_members", columns: []string{"user_id", "group_id", "deleted_at"}},
	{name: "idx_debates_status_created_live", table: "debates", columns: []string{"status", "created_at", "id", "deleted_at"}},
	{name: "idx_timeline_events_public_date", table: "timeline_events", columns: []string{"event_date", "id", "deleted_at", "is_public"}},
	{name: "idx_timeline_persons_public_name", table: "timeline_persons", columns: []string{"name", "id", "deleted_at", "is_public"}},
	{name: "idx_feed_item_stars_item", table: "feed_item_stars", columns: []string{"feed_item_id"}},
	{name: "idx_feed_items_fulltext_eligible", table: "feed_items", columns: []string{"full_text_status", "next_full_text_attempt_at", "created_at", "published_at", "feed_source_id", "id", "deleted_at", "enclosure_url", "enclosure_type", "duration"}},
	{name: "idx_feed_items_fulltext_ready", table: "feed_items", columns: []string{"next_full_text_attempt_at", "full_text_status", "created_at", "published_at", "feed_source_id", "id", "deleted_at", "enclosure_url", "enclosure_type", "duration"}},
	{name: "idx_feed_items_reader_backfill_published", table: "feed_items", columns: []string{"published_at", "id", "feed_source_id", "deleted_at", "reader_version", "feed_content_html", "full_text_html", "reader_html", "full_text_status"}},
	{name: "idx_feed_items_fulltext_stale", table: "feed_items", columns: []string{"last_full_text_attempt_at", "id", "deleted_at", "full_text_status"}},
	{name: "idx_content_entries_published_order", table: "content_entries", columns: []string{"kind", "published_at", "created_at", "id", "deleted_at", "status"}},
	{name: "idx_content_entries_owner_channel_live", table: "content_entries", columns: []string{"author_id", "channel_id", "created_at", "id", "deleted_at"}},
	{name: "idx_content_collections_channel_name_live", table: "content_collections", columns: []string{"channel_id", "name", "id", "deleted_at"}},
}

func searchQuerySchemaTableExists(db *gorm.DB, table string) bool {
	var exists bool
	if err := db.Raw(`
			SELECT EXISTS (
				SELECT 1
				FROM pg_catalog.pg_class AS classes
				JOIN pg_catalog.pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
				WHERE namespaces.nspname = current_schema() AND classes.relname = ?
			)`, table).Scan(&exists).Error; err != nil {
		return false
	}
	return exists
}

func searchQueryIndexAvailable(statement string, columnsByTable map[string]map[string]struct{}) bool {
	for _, requirement := range searchQueryIndexRequirements {
		if !strings.Contains(statement, requirement.name) {
			continue
		}
		columns, ok := columnsByTable[requirement.table]
		if !ok || columns == nil {
			return false
		}
		for _, column := range requirement.columns {
			if _, ok := columns[column]; !ok {
				return false
			}
		}
		return true
	}
	return false
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
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_items_fulltext_eligible
			ON feed_items (full_text_status, next_full_text_attempt_at, created_at, published_at, feed_source_id, id)
			WHERE deleted_at IS NULL
				AND COALESCE(enclosure_url, '') = ''
				AND COALESCE(enclosure_type, '') NOT LIKE 'audio/%'
				AND COALESCE(enclosure_type, '') NOT LIKE 'video/%'
				AND COALESCE(duration, '') = ''`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_items_fulltext_ready
			ON feed_items (next_full_text_attempt_at, full_text_status, created_at, published_at, feed_source_id, id)
			WHERE deleted_at IS NULL
				AND COALESCE(enclosure_url, '') = ''
				AND COALESCE(enclosure_type, '') NOT LIKE 'audio/%'
				AND COALESCE(enclosure_type, '') NOT LIKE 'video/%'
				AND COALESCE(duration, '') = ''`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_items_reader_backfill_published
			ON feed_items (published_at DESC, id DESC, feed_source_id)
			WHERE deleted_at IS NULL
				AND reader_version < 2
				AND (
					COALESCE(feed_content_html, '') <> ''
					OR COALESCE(full_text_html, '') <> ''
					OR (COALESCE(reader_html, '') = '' AND full_text_status IN ('disabled', 'failed'))
				)`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_items_fulltext_stale
			ON feed_items (last_full_text_attempt_at, id)
			WHERE deleted_at IS NULL AND full_text_status = 'fetching'`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_entries_published_order
			ON content_entries (kind, published_at DESC, created_at DESC, id DESC)
			WHERE deleted_at IS NULL AND status = 'published'`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_entries_owner_channel_live
			ON content_entries (author_id, channel_id, created_at DESC, id DESC)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_collections_channel_name_live
			ON content_collections (channel_id, LOWER(name), id)
			WHERE deleted_at IS NULL`,
	}
}
