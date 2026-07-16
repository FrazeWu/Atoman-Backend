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
		&model.DiscussionTarget{},
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
	if err := db.Create(&model.DiscussionTarget{
		Kind: "blog_post", ResourceID: lively.ID, ResourceKey: lively.ID.String(), CommentCount: 1, RootCount: 1,
	}).Error; err != nil {
		t.Fatalf("create discussion target: %v", err)
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

func TestHotContentUsesReachableTargetPaths(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Post{},
		&model.Video{},
		&model.FeedItem{},
		&model.PodcastEpisode{},
	)

	userID := uuid.Must(uuid.NewV7())
	if err := db.Create(&model.User{
		UUID:     userID,
		Username: "owner",
		Email:    "owner@example.com",
		Password: "pw",
		IsActive: true,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	post := model.Post{
		UserID:     userID,
		Title:      "Podcast post",
		Content:    "content",
		Status:     "published",
		Visibility: "public",
	}
	video := model.Video{
		Title:       "Portal video",
		Description: "desc",
		Status:      "published",
		Visibility:  "public",
		ViewCount:   42,
	}
	feedItem := model.FeedItem{
		Title:   "Portal feed item",
		Summary: "summary",
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	if err := db.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	if err := db.Create(&feedItem).Error; err != nil {
		t.Fatalf("create feed item: %v", err)
	}

	episode := model.PodcastEpisode{
		PostID: post.ID,
	}
	if err := db.Create(&episode).Error; err != nil {
		t.Fatalf("create episode: %v", err)
	}

	response, err := NewService(db).HotContent(6)
	if err != nil {
		t.Fatalf("HotContent returned error: %v", err)
	}

	pathsByModule := map[string][]string{}
	for _, item := range response.Featured {
		pathsByModule[item.Module] = append(pathsByModule[item.Module], item.TargetPath)
	}
	for _, section := range response.Sections {
		for _, item := range section.Items {
			pathsByModule[item.Module] = append(pathsByModule[item.Module], item.TargetPath)
		}
	}

	assertContainsPath(t, pathsByModule["video"], "/videos/watch/"+video.ID.String())
	assertContainsPath(t, pathsByModule["feed"], "/feed/item/"+feedItem.ID.String())
	assertContainsPath(t, pathsByModule["podcast"], "/podcasts/episode/"+episode.ID.String())
}

func assertContainsPath(t *testing.T, paths []string, expected string) {
	t.Helper()
	for _, path := range paths {
		if path == expected {
			return
		}
	}
	t.Fatalf("expected path %q in %v", expected, paths)
}
