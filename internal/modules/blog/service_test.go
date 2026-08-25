package blog

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"gorm.io/gorm"
)

func newBlogScopeTest(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser, model.Channel) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Channel{}, &model.Collection{}, &model.Post{}, &model.PodcastEpisode{},
		&model.ContentEntry{}, &model.ContentPostExtension{}, &model.ContentBlogExtension{},
		&model.ContentBlogVersion{}, &model.ContentBlogDraft{}, &model.ContentCollection{},
		&model.ContentCollectionMembership{}, &model.LegacyCollectionMapping{},
		&model.ContentPublicationEvent{}, &model.ContentReference{}, &model.ContentMediaAsset{},
	)
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

func TestBlogChannelSlugsAreNormalizedAndDisambiguated(t *testing.T) {
	service, _, user, _ := newBlogScopeTest(t)
	first, err := service.CreateChannel(user, "First", "shared studio", "", "")
	if err != nil {
		t.Fatalf("create first channel: %v", err)
	}
	if first.Slug != "shared-studio-2" {
		t.Fatalf("expected normalized disambiguated slug, got %q", first.Slug)
	}
	second, err := service.CreateChannel(user, "Second", "shared studio", "", "")
	if err != nil {
		t.Fatalf("create second channel: %v", err)
	}
	if second.Slug != "shared-studio-3" {
		t.Fatalf("expected second disambiguated slug, got %q", second.Slug)
	}
	updated, err := service.UpdateChannel(user, second.ID, second.Name, "shared studio", second.Description, second.CoverURL)
	if err != nil {
		t.Fatalf("update channel slug: %v", err)
	}
	if updated.Slug != second.Slug {
		t.Fatalf("expected update to retain the current available slug, got %q", updated.Slug)
	}
}

func TestDraftAllowsNoCollectionAndPublishUsesSystemDefault(t *testing.T) {
	service, db, user, channel := newBlogScopeTest(t)
	draft, err := service.CreatePost(user, CreatePostRequest{
		ChannelID: channel.ID, Title: "Draft", Content: "body", Status: "draft",
	})
	if err != nil {
		t.Fatalf("create collectionless draft: %v", err)
	}
	if draft.CollectionID != nil {
		t.Fatalf("expected draft collection to be nil, got %s", *draft.CollectionID)
	}

	published, err := service.CreatePost(user, CreatePostRequest{
		ChannelID: channel.ID, Title: "Published", Content: "body", Status: "published",
	})
	if err != nil {
		t.Fatalf("publish with system default collection: %v", err)
	}
	if published.CollectionID == nil {
		t.Fatal("expected system default collection to be assigned")
	}
	var collection model.ContentCollection
	if err := db.First(&collection, "id = ?", *published.CollectionID).Error; err != nil {
		t.Fatal(err)
	}
	if !collection.IsDefault {
		t.Fatalf("expected system default collection, got %#v", collection)
	}
}

func TestBlogPublishUsesCanonicalCollection(t *testing.T) {
	service, db, user, channel := newBlogScopeTest(t)
	collection := model.ContentCollection{ChannelID: channel.ID, Name: "Videos"}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	post, err := service.CreatePost(user, CreatePostRequest{
		ChannelID: channel.ID, CollectionID: collection.ID,
		Title: "Canonical collection", Content: "body", Status: "published",
	})
	if err != nil {
		t.Fatalf("publish with canonical collection: %v", err)
	}
	if post.CollectionID == nil || *post.CollectionID != collection.ID {
		t.Fatalf("expected canonical collection %s, got %#v", collection.ID, post.CollectionID)
	}
}
