package portal

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestHotContentOrdersFeaturedBlogPostsByEngagement(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Post{},
		&model.Like{},
		&model.Comment{},
	)

	userID := uuid.Must(uuid.NewV7())
	if err := db.Create(&model.User{
		UUID:     userID,
		Username: "reader",
		Email:    "reader@example.com",
		Password: "pw",
		IsActive: true,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	quiet := model.Post{
		UserID:     userID,
		Title:      "Quiet note",
		Content:    "quiet content",
		Status:     "published",
		Visibility: "public",
	}
	lively := model.Post{
		UserID:     userID,
		Title:      "Lively note",
		Content:    "lively content",
		Status:     "published",
		Visibility: "public",
		Summary:    "A lively post",
		CoverURL:   "/covers/lively.jpg",
	}
	if err := db.Create(&quiet).Error; err != nil {
		t.Fatalf("create quiet post: %v", err)
	}
	if err := db.Create(&lively).Error; err != nil {
		t.Fatalf("create lively post: %v", err)
	}

	if err := db.Create(&model.Like{UserID: userID, TargetType: "post", TargetID: lively.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Create(&model.Comment{
		TargetType: "post",
		TargetID:   lively.ID,
		UserID:     model.NewNullableUserUUID(userID),
		Content:    "Great",
		Status:     "visible",
	}).Error; err != nil {
		t.Fatalf("create comment: %v", err)
	}

	response, err := NewService(db).HotContent(4)
	if err != nil {
		t.Fatalf("HotContent returned error: %v", err)
	}
	if len(response.Featured) == 0 {
		t.Fatal("expected featured items")
	}
	if response.Featured[0].Title != "Lively note" {
		t.Fatalf("expected lively post first, got %q", response.Featured[0].Title)
	}
	if response.Featured[0].Module != "blog" {
		t.Fatalf("expected blog module, got %q", response.Featured[0].Module)
	}
	if response.Featured[0].TargetPath != "/posts/post/"+lively.ID.String() {
		t.Fatalf("unexpected target path: %q", response.Featured[0].TargetPath)
	}
	if response.Featured[0].ImageURL != "/covers/lively.jpg" {
		t.Fatalf("unexpected image URL: %q", response.Featured[0].ImageURL)
	}
}

func TestHotContentReturnsEmptyResponseWhenNoContentExists(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Post{})

	response, err := NewService(db).HotContent(4)
	if err != nil {
		t.Fatalf("HotContent returned error: %v", err)
	}
	if len(response.Featured) != 0 {
		t.Fatalf("expected no featured items, got %d", len(response.Featured))
	}
	if len(response.Sections) != 0 {
		t.Fatalf("expected no sections, got %d", len(response.Sections))
	}
}
