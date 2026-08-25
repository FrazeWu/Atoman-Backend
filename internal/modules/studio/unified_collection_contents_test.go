package studio

import (
	"testing"

	"atoman/internal/model"
)

func TestStudioUnifiedCollectionCandidatesMoveAndRemoveContent(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	target := fixture.collections[ModuleBlog]
	other := model.Collection{ChannelID: fixture.channel.ID, ContentType: string(ModuleBlog), Name: "other collection"}
	if err := fixture.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	moved := model.Post{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionID: &other.ID,
		Title: "Move this article", Content: "body", Status: "draft", Visibility: "public",
	}
	if err := fixture.db.Create(&moved).Error; err != nil {
		t.Fatal(err)
	}
	unassigned := model.Post{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID,
		Title: "Unassigned article", Content: "body", Status: "draft", Visibility: "public",
	}
	if err := fixture.db.Create(&unassigned).Error; err != nil {
		t.Fatal(err)
	}

	candidates, err := fixture.service.ListUnifiedCollectionCandidates(fixture.user, target.ID, "Move this")
	if err != nil {
		t.Fatalf("list collection candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ContentID != moved.ID || candidates[0].CurrentCollectionID == nil || *candidates[0].CurrentCollectionID != other.ID {
		t.Fatalf("expected moved candidate with current collection, got %#v", candidates)
	}
	if err := fixture.service.AddUnifiedCollectionContent(fixture.user, target.ID, moved.ID); err != nil {
		t.Fatalf("move content into target collection: %v", err)
	}
	var memberships []model.ContentCollectionMembership
	if err := fixture.db.Where("content_id = ?", moved.ID).Find(&memberships).Error; err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 1 || memberships[0].CollectionID != target.ID {
		t.Fatalf("expected exactly one target membership, got %#v", memberships)
	}
	contents, err := fixture.service.ListUnifiedCollectionContents(fixture.user, target.ID)
	if err != nil {
		t.Fatalf("list target collection contents: %v", err)
	}
	if len(contents) != 1 || contents[0].ContentID != moved.ID || contents[0].ID != moved.ID {
		t.Fatalf("expected moved content in target collection, got %#v", contents)
	}
	if err := fixture.service.RemoveUnifiedCollectionContent(fixture.user, target.ID, moved.ID); err != nil {
		t.Fatalf("remove content from target collection: %v", err)
	}
	if err := fixture.db.Where("content_id = ?", moved.ID).Find(&memberships).Error; err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 0 {
		t.Fatalf("expected no membership after removal, got %#v", memberships)
	}
	candidates, err = fixture.service.ListUnifiedCollectionCandidates(fixture.user, target.ID, "Unassigned")
	if err != nil {
		t.Fatalf("list unassigned candidate: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ContentID != unassigned.ID || candidates[0].CurrentCollectionID != nil {
		t.Fatalf("expected unassigned candidate, got %#v", candidates)
	}
}
