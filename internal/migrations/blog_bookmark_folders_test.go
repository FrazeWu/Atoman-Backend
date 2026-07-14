package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunBlogBookmarkFolderMigrationClassifiesLegacyBookmarks(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Post{}, &model.BookmarkFolder{}, &model.Bookmark{})
	user := model.User{Username: "bookmark-user", Email: "bookmark@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	post := model.Post{UserID: user.UUID, Title: "Post", Content: "body", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	bookmark := model.Bookmark{UserID: user.UUID, PostID: post.ID}
	if err := db.Create(&bookmark).Error; err != nil {
		t.Fatalf("create bookmark: %v", err)
	}

	if err := RunBlogBookmarkFolderMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if err := RunBlogBookmarkFolderMigration(db); err != nil {
		t.Fatalf("rerun migration: %v", err)
	}
	if err := db.First(&bookmark, "id = ?", bookmark.ID).Error; err != nil {
		t.Fatalf("reload bookmark: %v", err)
	}
	if bookmark.BookmarkFolderID == nil {
		t.Fatal("expected default folder assignment")
	}
	var folder model.BookmarkFolder
	if err := db.First(&folder, "id = ?", *bookmark.BookmarkFolderID).Error; err != nil || folder.Name != "默认收藏夹" {
		t.Fatalf("unexpected default folder: %#v err=%v", folder, err)
	}
}

func TestRunBlogBookmarkFolderMigrationRenamesLegacyDefaultFolder(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Post{}, &model.BookmarkFolder{}, &model.Bookmark{})
	user := model.User{Username: "legacy-folder-user", Email: "legacy-folder@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	post := model.Post{UserID: user.UUID, Title: "Post", Content: "body", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	folder := model.BookmarkFolder{UserID: user.UUID, Name: "默认收藏"}
	if err := db.Create(&folder).Error; err != nil {
		t.Fatalf("create legacy folder: %v", err)
	}
	bookmark := model.Bookmark{UserID: user.UUID, PostID: post.ID, BookmarkFolderID: &folder.ID}
	if err := db.Create(&bookmark).Error; err != nil {
		t.Fatalf("create bookmark: %v", err)
	}

	if err := RunBlogBookmarkFolderMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	var migratedFolder model.BookmarkFolder
	if err := db.First(&migratedFolder, "id = ?", folder.ID).Error; err != nil {
		t.Fatalf("load migrated folder: %v", err)
	}
	if migratedFolder.Name != "默认收藏夹" {
		t.Fatalf("expected normalized default folder name, got %q", migratedFolder.Name)
	}
	var migratedBookmark model.Bookmark
	if err := db.First(&migratedBookmark, "id = ?", bookmark.ID).Error; err != nil {
		t.Fatalf("load migrated bookmark: %v", err)
	}
	if migratedBookmark.BookmarkFolderID == nil || *migratedBookmark.BookmarkFolderID != migratedFolder.ID {
		t.Fatalf("expected bookmark to remain in migrated folder, got %#v", migratedBookmark.BookmarkFolderID)
	}
}
