package migrations

import (
	"testing"

	"github.com/google/uuid"

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

func TestRunBlogInteractionUniqueIndexesDeduplicatesExistingRows(t *testing.T) {
	db := testdb.Open(t)

	if err := db.Exec(`
CREATE TABLE likes (
	id text PRIMARY KEY,
	created_at datetime,
	updated_at datetime,
	deleted_at datetime,
	user_id text NOT NULL,
	target_type text NOT NULL,
	target_id text NOT NULL
);
`).Error; err != nil {
		t.Fatalf("create legacy likes table: %v", err)
	}
	if err := db.Exec(`
CREATE TABLE bookmarks (
	id text PRIMARY KEY,
	created_at datetime,
	updated_at datetime,
	deleted_at datetime,
	user_id text NOT NULL,
	post_id text NOT NULL,
	bookmark_folder_id text
);
`).Error; err != nil {
		t.Fatalf("create legacy bookmarks table: %v", err)
	}

	userID := uuid.NewString()
	postID := uuid.NewString()
	if err := db.Exec(`INSERT INTO likes (id, user_id, target_type, target_id) VALUES (?, ?, 'post', ?), (?, ?, 'post', ?)`,
		uuid.NewString(), userID, postID,
		uuid.NewString(), userID, postID,
	).Error; err != nil {
		t.Fatalf("seed duplicate likes: %v", err)
	}
	if err := db.Exec(`INSERT INTO bookmarks (id, user_id, post_id) VALUES (?, ?, ?), (?, ?, ?)`,
		uuid.NewString(), userID, postID,
		uuid.NewString(), userID, postID,
	).Error; err != nil {
		t.Fatalf("seed duplicate bookmarks: %v", err)
	}

	if err := RunBlogInteractionUniqueIndexes(db); err != nil {
		t.Fatalf("run blog interaction unique indexes migration: %v", err)
	}

	var likeCount int64
	if err := db.Model(&model.Like{}).Where("user_id = ? AND target_type = ? AND target_id = ?", userID, "post", postID).Count(&likeCount).Error; err != nil {
		t.Fatalf("count likes: %v", err)
	}
	if likeCount != 1 {
		t.Fatalf("expected 1 like after dedupe, got %d", likeCount)
	}

	var bookmarkCount int64
	if err := db.Model(&model.Bookmark{}).Where("user_id = ? AND post_id = ?", userID, postID).Count(&bookmarkCount).Error; err != nil {
		t.Fatalf("count bookmarks: %v", err)
	}
	if bookmarkCount != 1 {
		t.Fatalf("expected 1 bookmark after dedupe, got %d", bookmarkCount)
	}
}
