package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/stretchr/testify/require"
)

func TestFeedFullTextSearchIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	require.NoError(t, RunFeedFullTextSearchIndexes(db))
	require.NoError(t, RunFeedFullTextSearchIndexes(db))

	var plain string
	require.NoError(t, db.Raw(
		`SELECT public.atoman_feed_search_document_v1(?, ?, ?, ?, ?, ?)`,
		"Title", "summary", "", "<p>Body Phrase</p>", "", "",
	).Scan(&plain).Error)
	require.Contains(t, plain, "body phrase")
	require.NotContains(t, plain, "<p>")

	var indexName string
	require.NoError(t, db.Raw(`SELECT indexname FROM pg_indexes WHERE indexname = 'idx_feed_items_search_document_trgm_v1'`).Scan(&indexName).Error)
	require.Equal(t, "idx_feed_items_search_document_trgm_v1", indexName)

	source := model.FeedSource{SourceType: "external_rss", Hash: "fulltext-index", Title: "Indexed"}
	require.NoError(t, db.Create(&source).Error)
	match := model.FeedItem{FeedSourceID: source.ID, GUID: "match", Title: "other", FullTextHTML: "<p>Body Phrase inside markup</p>"}
	miss := model.FeedItem{FeedSourceID: source.ID, GUID: "miss", Title: "other", FullTextHTML: "<p>unrelated</p>"}
	require.NoError(t, db.Create(&match).Error)
	require.NoError(t, db.Create(&miss).Error)
	var ids []string
	require.NoError(t, db.Raw(`SELECT id::text FROM feed_items
		WHERE deleted_at IS NULL
		  AND public.atoman_feed_search_document_v1(title, summary, author, reader_html, full_text_html, feed_content_html) LIKE ?`,
		"%body phrase%").Scan(&ids).Error)
	require.Equal(t, []string{match.ID.String()}, ids)
}
