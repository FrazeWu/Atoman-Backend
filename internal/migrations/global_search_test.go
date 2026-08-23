package migrations

import (
	"strings"
	"testing"
)

func TestGlobalSearchIndexStatementsCoverCrossModuleSearchFields(t *testing.T) {
	statements := globalSearchIndexStatements()
	if len(statements) != 25 {
		t.Fatalf("expected extension plus twenty-four global search indexes, got %d", len(statements))
	}

	joined := strings.Join(statements, "\n")
	for _, fragment := range []string{
		"CREATE EXTENSION IF NOT EXISTS pg_trgm",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_username_trgm",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_display_name_trgm",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_short_notes_content_trgm",
		"idx_debates_title_trgm",
		"idx_feed_sources_title_trgm",
		"idx_feed_items_title_trgm",
		"idx_channels_name_trgm",
		"idx_channels_slug_trgm",
		"idx_collections_name_trgm",
		"idx_videos_title_trgm",
		"idx_timeline_persons_name_trgm",
		"idx_timeline_events_title_trgm",
		"idx_debates_description_trgm",
		"idx_debates_content_trgm",
		"idx_feed_sources_rss_url_trgm",
		"idx_feed_items_summary_trgm",
		"idx_videos_description_trgm",
		"idx_artist_aliases_alias_trgm",
		"idx_music_song_lyrics_content_trgm",
		"idx_music_song_lyrics_translation_trgm",
		"idx_timeline_persons_bio_trgm",
		"idx_timeline_events_description_trgm",
		"idx_timeline_events_content_trgm",
		"idx_debates_tags_gin",
		"LOWER(title) gin_trgm_ops",
		"WHERE deleted_at IS NULL",
	} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("expected global search index SQL to contain %q, got: %s", fragment, joined)
		}
	}
}
