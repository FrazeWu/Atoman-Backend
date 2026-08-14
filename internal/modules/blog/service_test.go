package blog

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"gorm.io/gorm"
)

func newBlogScopeTest(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser, model.Channel) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Collection{}, &model.Post{}, &model.PodcastEpisode{}, &model.ContentPublicationEvent{}, &model.BlogPostVersion{})
	user := model.User{Username: "blog-scope", Email: "blog-scope@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{UserID: &user.UUID, Name: "Shared Studio", Slug: "shared-studio"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	return NewService(db), db, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}, channel
}

func TestDraftAllowsNoCollectionAndPublishRequiresCollection(t *testing.T) {
	service, _, user, channel := newBlogScopeTest(t)
	draft, err := service.CreatePost(user, CreatePostRequest{
		ChannelID: channel.ID, Title: "Draft", Content: "body", Status: "draft",
	})
	if err != nil {
		t.Fatalf("create collectionless draft: %v", err)
	}
	if draft.CollectionID != nil {
		t.Fatalf("expected draft collection to be nil, got %s", *draft.CollectionID)
	}

	_, err = service.CreatePost(user, CreatePostRequest{
		ChannelID: channel.ID, Title: "Published", Content: "body", Status: "published",
	})
	if app := apperr.FromError(err); app == nil || app.HTTPStatus != 400 {
		t.Fatalf("expected collectionless publish to return 400, got %v", err)
	}
}

func TestBlogPublishRejectsCollectionFromAnotherModule(t *testing.T) {
	service, db, user, channel := newBlogScopeTest(t)
	videoCollection := model.Collection{ChannelID: channel.ID, ContentType: "video", Name: "Videos"}
	if err := db.Create(&videoCollection).Error; err != nil {
		t.Fatal(err)
	}
	_, err := service.CreatePost(user, CreatePostRequest{
		ChannelID: channel.ID, CollectionID: videoCollection.ID,
		Title: "Wrong module", Content: "body", Status: "published",
	})
	if app := apperr.FromError(err); app == nil || app.HTTPStatus != 400 {
		t.Fatalf("expected wrong module collection to return 400, got %v", err)
	}
}
