package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

// RunGlobalSearchIndexes adds trigram indexes for fields queried by the
// cross-module reference search. Existing module-specific search migrations
// own the indexes for posts, forum content, music catalog entries, and
// comments; this migration covers the remaining global-search fields.
func RunGlobalSearchIndexes(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "pgx" {
		return nil
	}

	statements := globalSearchIndexStatements()
	if err := db.Exec(statements[0]).Error; err != nil {
		return fmt.Errorf("enable pg_trgm extension: %w", err)
	}

	var extensionSchema string
	if err := db.Raw(`SELECT n.nspname
		FROM pg_extension e
		JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = 'pg_trgm'`).Scan(&extensionSchema).Error; err != nil {
		return fmt.Errorf("resolve pg_trgm schema: %w", err)
	}
	if extensionSchema == "" {
		return fmt.Errorf("resolve pg_trgm schema: extension is not installed")
	}

	operatorClass := quotePostgresIdentifier(extensionSchema) + ".gin_trgm_ops"
	for _, statement := range globalSearchIndexStatements(operatorClass)[1:] {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("create global search index: %w", err)
		}
	}
	return nil
}

func globalSearchIndexStatements(operatorClass ...string) []string {
	trigramOperatorClass := "gin_trgm_ops"
	if len(operatorClass) > 0 {
		trigramOperatorClass = operatorClass[0]
	}
	return []string{
		`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_username_trgm
			ON "Users" USING GIN (LOWER(username) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_display_name_trgm
			ON "Users" USING GIN (LOWER(display_name) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_short_notes_content_trgm
			ON short_notes USING GIN (LOWER(content) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_debates_title_trgm
			ON debates USING GIN (LOWER(title) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_sources_title_trgm
			ON feed_sources USING GIN (LOWER(title) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_items_title_trgm
			ON feed_items USING GIN (LOWER(title) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_channels_name_trgm
			ON channels USING GIN (LOWER(name) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_channels_slug_trgm
			ON channels USING GIN (LOWER(slug) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_collections_name_trgm
			ON collections USING GIN (LOWER(name) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_videos_title_trgm
			ON videos USING GIN (LOWER(title) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_timeline_persons_name_trgm
			ON timeline_persons USING GIN (LOWER(name) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_timeline_events_title_trgm
			ON timeline_events USING GIN (LOWER(title) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_debates_description_trgm
			ON debates USING GIN (LOWER(description) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_debates_content_trgm
			ON debates USING GIN (LOWER(content) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_sources_rss_url_trgm
			ON feed_sources USING GIN (LOWER(rss_url) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_items_summary_trgm
			ON feed_items USING GIN (LOWER(summary) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_videos_description_trgm
			ON videos USING GIN (LOWER(description) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_artist_aliases_alias_trgm
			ON artist_aliases USING GIN (LOWER(alias) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_music_song_lyrics_content_trgm
			ON music_song_lyrics USING GIN (LOWER(content) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_music_song_lyrics_translation_trgm
			ON music_song_lyrics USING GIN (LOWER(translation) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_timeline_persons_bio_trgm
			ON timeline_persons USING GIN (LOWER(bio) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_timeline_events_description_trgm
			ON timeline_events USING GIN (LOWER(description) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_timeline_events_content_trgm
			ON timeline_events USING GIN (LOWER(content) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_entries_title_trgm
			ON content_entries USING GIN (LOWER(title) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_entries_summary_trgm
			ON content_entries USING GIN (LOWER(summary) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_collections_name_trgm
			ON content_collections USING GIN (LOWER(name) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_debates_tags_gin
			ON debates USING GIN (tags)
			WHERE deleted_at IS NULL`,
	}
}
