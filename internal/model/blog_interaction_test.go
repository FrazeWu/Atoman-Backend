package model

import (
	"testing"

	"atoman/internal/testdb"
)

func TestBlogLikeAndBookmarkAreUniquePerUserTarget(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &User{}, &Post{}, &Like{}, &Bookmark{})

	user := User{Username: "blog-interaction-user", Email: "blog-interaction@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	post := Post{UserID: user.UUID, Title: "Post", Content: "Body", Status: "published", Visibility: "public", AllowComments: true}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	if err := db.Create(&Like{UserID: user.UUID, TargetType: "post", TargetID: post.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Create(&Like{UserID: user.UUID, TargetType: "post", TargetID: post.ID}).Error; err == nil {
		t.Fatal("expected duplicate like to fail")
	}

	if err := db.Create(&Bookmark{UserID: user.UUID, PostID: post.ID}).Error; err != nil {
		t.Fatalf("create bookmark: %v", err)
	}
	if err := db.Create(&Bookmark{UserID: user.UUID, PostID: post.ID}).Error; err == nil {
		t.Fatal("expected duplicate bookmark to fail")
	}
}
