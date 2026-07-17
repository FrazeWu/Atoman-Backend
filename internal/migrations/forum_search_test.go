package migrations

import (
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestForumSearchIndexStatementsUsePartialGINIndexes(t *testing.T) {
	statements := forumSearchIndexStatements()
	if len(statements) != 6 {
		t.Fatalf("expected extension plus five search index statements, got %d", len(statements))
	}
	for _, fragment := range []string{
		"CREATE EXTENSION IF NOT EXISTS pg_trgm",
		"idx_forum_topics_search",
		"idx_forum_comments_search",
		"idx_forum_topics_title_trgm",
		"idx_forum_topics_content_trgm",
		"idx_forum_comments_content_trgm",
		"USING GIN",
		"to_tsvector('simple'",
		"gin_trgm_ops",
		"WHERE deleted_at IS NULL",
	} {
		joined := strings.Join(statements, "\n")
		if !strings.Contains(joined, fragment) {
			t.Fatalf("expected search index SQL to contain %q, got: %s", fragment, joined)
		}
	}
}

func TestRunForumSearchIndexesSkipsSQLite(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.ForumTopic{}, &model.CommentEntry{})

	if err := RunForumSearchIndexes(db); err != nil {
		t.Fatalf("expected SQLite migration to be skipped: %v", err)
	}
	if db.Migrator().HasIndex("forum_topics", "idx_forum_topics_search") || db.Migrator().HasIndex("comment_entries", "idx_forum_comments_search") {
		t.Fatal("expected PostgreSQL search indexes to remain absent on SQLite")
	}
}
