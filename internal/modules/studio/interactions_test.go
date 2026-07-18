package studio

import (
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func createStudioComment(t *testing.T, fixture studioQueryFixture, kind string, resourceID uuid.UUID, ownerID uuid.UUID, content string) model.CommentEntry {
	t.Helper()
	target := model.DiscussionTarget{
		Kind: kind, ResourceID: resourceID, ResourceKey: resourceID.String(), OwnerID: &ownerID,
		CommentCount: 1, RootCount: 1,
	}
	if err := fixture.db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	comment := model.CommentEntry{
		TargetID: target.ID, AuthorID: fixture.foreignUser.UUID,
		Content: content, ContentHash: uuid.NewString(), Status: "active",
	}
	if err := fixture.db.Create(&comment).Error; err != nil {
		t.Fatal(err)
	}
	return comment
}

func TestStudioInteractionsReturnOnlyOwnedChannelModuleComments(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	blogCollection := fixture.collections[ModuleBlog]
	ownedPost := model.Post{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionID: &blogCollection.ID,
		Title: "Owned article", Content: "body", Status: "published", Visibility: "public",
	}
	if err := fixture.db.Create(&ownedPost).Error; err != nil {
		t.Fatal(err)
	}
	ownedComment := createStudioComment(t, fixture, "blog_post", ownedPost.ID, fixture.user.ID, "owned comment")
	foreignChannel := model.Channel{UserID: &fixture.foreignUser.UUID, Name: "Foreign Interactions", Slug: "foreign-interactions-" + uuid.NewString()[:8]}
	if err := fixture.db.Create(&foreignChannel).Error; err != nil {
		t.Fatal(err)
	}
	foreignPost := model.Post{
		UserID: fixture.foreignUser.UUID, ChannelID: &foreignChannel.ID,
		Title: "Foreign article", Content: "body", Status: "published", Visibility: "public",
	}
	if err := fixture.db.Create(&foreignPost).Error; err != nil {
		t.Fatal(err)
	}
	createStudioComment(t, fixture, "blog_post", foreignPost.ID, fixture.foreignUser.UUID, "foreign comment")

	items, total, err := fixture.service.ListInteractions(fixture.user, ModuleBlog, InteractionQuery{ChannelID: fixture.channel.ID})
	if err != nil {
		t.Fatalf("list interactions: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != ownedComment.ID || items[0].ContentID != ownedPost.ID {
		t.Fatalf("expected only owned blog interaction, total=%d items=%#v", total, items)
	}
}

func TestStudioVideoInteractionsFilterTimestampComments(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	video := model.Video{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, Title: "Anchored Video",
		StorageType: "local", VideoURL: "video.mp4", Status: "published", Visibility: "public",
	}
	if err := fixture.db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	anchored := createStudioComment(t, fixture, "video", video.ID, fixture.user.ID, "anchored")
	createStudioComment(t, fixture, "video", video.ID, fixture.user.ID, "plain")
	if err := fixture.db.Create(&model.CommentTimeAnchor{CommentID: anchored.ID, StartOffset: 0, EndOffset: 4, Seconds: 42}).Error; err != nil {
		t.Fatal(err)
	}

	items, total, err := fixture.service.ListInteractions(fixture.user, ModuleVideo, InteractionQuery{
		ChannelID: fixture.channel.ID, Anchored: true,
	})
	if err != nil {
		t.Fatalf("list anchored interactions: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != anchored.ID || len(items[0].TimeAnchors) != 1 || items[0].TimeAnchors[0].Seconds != 42 {
		t.Fatalf("expected only timestamp comment, total=%d items=%#v", total, items)
	}
}
