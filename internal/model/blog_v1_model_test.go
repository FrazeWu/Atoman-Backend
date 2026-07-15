package model

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"atoman/internal/testdb"
)

func TestBlogV1ModelsMigrateAndCreateRating(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&User{},
		&Channel{},
		&Collection{},
		&Post{},
		&BlogPostRating{},
		&MediaAsset{},
	)

	ownerID := uuid.Must(uuid.NewV7())
	creatorID := uuid.Must(uuid.NewV7())
	user := User{
		UUID:     ownerID,
		Username: "owner",
		Email:    "owner@example.com",
		Password: "pw",
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	creator := User{
		UUID:     creatorID,
		Username: "creator",
		Email:    "creator@example.com",
		Password: "pw",
		IsActive: true,
	}
	if err := db.Create(&creator).Error; err != nil {
		t.Fatalf("create creator: %v", err)
	}

	banUntil := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	channel := Channel{
		UserID:      &ownerID,
		Name:        "纸墨频道",
		Slug:        "paper-ink",
		Description: "channel description",
		IsAnonymous: true,
		BanUntil:    &banUntil,
		BanReason:   "temporary moderation",
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	collection := Collection{
		ChannelID:   channel.ID,
		CreatedBy:   &creatorID,
		Name:        "精选",
		Description: "collection description",
	}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatalf("create collection: %v", err)
	}

	post := Post{
		UserID:             ownerID,
		ChannelID:          &channel.ID,
		Title:              "A rated post",
		Content:            "content",
		Status:             "published",
		Visibility:         "public",
		RatingAverageScore: 4,
		RatingCount:        1,
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	rating := BlogPostRating{
		PostID:  post.ID,
		UserID:  creatorID,
		Score:   4,
		Comment: "值得一读",
	}
	if err := db.Create(&rating).Error; err != nil {
		t.Fatalf("create rating: %v", err)
	}

	asset := MediaAsset{
		UserID:      &creatorID,
		Purpose:     "blog.image",
		URL:         "/uploads/blog/cover/example.jpg",
		Key:         "blog/cover/example.jpg",
		ContentType: "image/jpeg",
		Size:        128,
	}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatalf("create media asset: %v", err)
	}

	if channel.ID == uuid.Nil {
		t.Fatal("expected channel ID to be generated")
	}
	if collection.ID == uuid.Nil {
		t.Fatal("expected collection ID to be generated")
	}
	if post.ID == uuid.Nil {
		t.Fatal("expected post ID to be generated")
	}
	if rating.ID == uuid.Nil {
		t.Fatal("expected rating ID to be generated")
	}
	if asset.ID == uuid.Nil {
		t.Fatal("expected media asset ID to be generated")
	}
}
