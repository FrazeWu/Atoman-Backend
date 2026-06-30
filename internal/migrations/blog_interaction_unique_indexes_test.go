package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunBlogInteractionUniqueIndexesCreatesExpectedIndexes(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Post{}, &model.Like{}, &model.Bookmark{})

	if err := RunBlogInteractionUniqueIndexes(db); err != nil {
		t.Fatalf("run blog interaction unique indexes migration: %v", err)
	}

	assertIndexExists(t, db, "likes", "idx_likes_user_target")
	assertIndexExists(t, db, "bookmarks", "idx_bookmarks_user_post")
}

func TestDeduplicateBlogInteractionsSkipsMissingTables(t *testing.T) {
	db := testdb.Open(t)

	if err := DeduplicateBlogInteractions(db); err != nil {
		t.Fatalf("expected missing tables to be skipped, got %v", err)
	}
}
