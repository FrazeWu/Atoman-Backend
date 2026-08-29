package migrations

import (
	"strings"
	"testing"

	"atoman/internal/testdb"
)

func TestBlogSearchIndexStatementsCoverCanonicalSubstringSearchColumns(t *testing.T) {
	joined := strings.Join(blogSearchIndexStatements(), "\n")
	for _, expected := range []string{
		"CREATE EXTENSION IF NOT EXISTS pg_trgm",
		"idx_content_entries_blog_title_trgm",
		"idx_content_entries_blog_summary_trgm",
		"idx_content_blog_extensions_content_trgm",
		"idx_content_blog_extensions_search_vector",
		"idx_content_entries_blog_search_vector",
		"idx_content_entries_blog_public_channel_published",
		"ON content_entries USING GIN (LOWER(title) gin_trgm_ops)",
		"ON content_blog_extensions USING GIN (LOWER(content) gin_trgm_ops)",
		"kind = 'blog' AND status = 'published'",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected canonical blog search indexes to contain %q", expected)
		}
	}
}

func TestRunBlogSearchIndexesSkipsSQLite(t *testing.T) {
	db := testdb.Open(t)
	if err := RunBlogSearchIndexes(db); err != nil {
		t.Fatalf("expected sqlite migration to be skipped: %v", err)
	}
}
