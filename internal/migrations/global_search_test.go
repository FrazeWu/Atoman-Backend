package migrations

import (
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/stretchr/testify/require"
)

func TestGlobalSearchIndexStatementsCoverCrossModuleSearchFields(t *testing.T) {
	statements := globalSearchIndexStatements()
	if len(statements) != 29 {
		t.Fatalf("expected extension plus twenty-eight global search indexes, got %d", len(statements))
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
		"idx_feed_items_author_trgm",
		"LOWER(author) gin_trgm_ops",
		"idx_videos_description_trgm",
		"idx_artist_aliases_alias_trgm",
		"idx_music_song_lyrics_content_trgm",
		"idx_music_song_lyrics_translation_trgm",
		"idx_timeline_persons_bio_trgm",
		"idx_timeline_events_description_trgm",
		"idx_timeline_events_content_trgm",
		"idx_content_entries_title_trgm",
		"idx_content_entries_summary_trgm",
		"idx_content_collections_name_trgm",
		"idx_debates_tags_gin",
		"LOWER(title) gin_trgm_ops",
		"WHERE deleted_at IS NULL",
	} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("expected global search index SQL to contain %q, got: %s", fragment, joined)
		}
	}
}

func TestFeedArticleTrigramIndexesSupportAllSearchFields(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	source := model.FeedSource{SourceType: "external_rss", Hash: "index-plan"}
	require.NoError(t, db.Create(&source).Error)
	// 给规划器足够的测试数据，避免空表的 deleted_at 索引扫描掩盖三元组索引。
	require.NoError(t, db.Exec(`INSERT INTO feed_items (id, feed_source_id, guid, title, summary, author)
		SELECT gen_random_uuid(), ?, n::text, md5(n::text), md5(n::text), md5(n::text)
		FROM generate_series(1, 5000) n`, source.ID).Error)
	operatorClass, err := pgTrgmOperatorClass(db)
	require.NoError(t, err)
	for _, statement := range globalSearchIndexStatements(operatorClass) {
		if strings.Contains(statement, "idx_feed_items_") {
			require.NoError(t, db.Exec(statement).Error)
			// 同一迁移重复运行仍须成功。
			require.NoError(t, db.Exec(statement).Error)
		}
	}
	require.NoError(t, db.Exec("ANALYZE feed_items").Error)
	var plan []string
	require.NoError(t, db.Raw(`EXPLAIN SELECT id FROM feed_items WHERE deleted_at IS NULL AND
		(LOWER(title) LIKE LOWER(?) OR LOWER(summary) LIKE LOWER(?) OR LOWER(author) LIKE LOWER(?))`,
		"%MiXeD%", "%MiXeD%", "%MiXeD%").Scan(&plan).Error)
	for _, field := range []string{"title", "summary", "author"} {
		require.Contains(t, strings.Join(plan, "\n"), "Bitmap Index Scan on idx_feed_items_"+field+"_trgm")
	}
}
