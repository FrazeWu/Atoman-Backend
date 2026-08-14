package migrations

import (
	"strings"
	"testing"

	"atoman/internal/testdb"
)

func TestBlogSearchIndexStatementsCoverSubstringSearchColumns(t *testing.T) {
	joined := strings.Join(blogSearchIndexStatements(), "\n")
	for _, expected := range []string{
		"CREATE EXTENSION IF NOT EXISTS pg_trgm",
		"LOWER(title) gin_trgm_ops",
		"LOWER(summary) gin_trgm_ops",
		"LOWER(content) gin_trgm_ops",
		"WHERE deleted_at IS NULL",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected blog search indexes to contain %q", expected)
		}
	}
}

func TestRunBlogSearchIndexesSkipsSQLite(t *testing.T) {
	db := testdb.Open(t)
	if err := RunBlogSearchIndexes(db); err != nil {
		t.Fatalf("expected sqlite migration to be skipped: %v", err)
	}
}
