package studio

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
)

func TestStudioSettingsRejectForeignDefaultCollection(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	foreignChannel := model.Channel{UserID: &fixture.foreignUser.UUID, Name: "Foreign Settings", Slug: "foreign-settings-" + uuid.NewString()[:8]}
	if err := fixture.db.Create(&foreignChannel).Error; err != nil {
		t.Fatal(err)
	}
	foreignCollection := model.Collection{ChannelID: foreignChannel.ID, ContentType: string(ModuleBlog), Name: "Foreign Articles"}
	if err := fixture.db.Create(&foreignCollection).Error; err != nil {
		t.Fatal(err)
	}

	_, err := fixture.service.SaveSettings(fixture.user, ModuleBlog, SettingsInput{
		ChannelID: fixture.channel.ID, DefaultCollectionID: &foreignCollection.ID,
	})
	appErr := apperr.FromError(err)
	if appErr == nil || appErr.HTTPStatus != 403 {
		t.Fatalf("expected foreign default collection 403, got %v", err)
	}
}

func TestStudioSettingsAreScopedByUserChannelAndModule(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	secondChannel := model.Channel{UserID: &fixture.user.ID, Name: "Second Settings", Slug: "second-settings-" + uuid.NewString()[:8]}
	if err := fixture.db.Create(&secondChannel).Error; err != nil {
		t.Fatal(err)
	}
	secondCollection := model.Collection{ChannelID: secondChannel.ID, ContentType: string(ModuleBlog), Name: "Second Articles"}
	if err := fixture.db.Create(&secondCollection).Error; err != nil {
		t.Fatal(err)
	}
	visibility := "subscribers"
	publishStatus := "draft"
	autoplay := true
	blogCollection := fixture.collections[ModuleBlog]
	saved, err := fixture.service.SaveSettings(fixture.user, ModuleBlog, SettingsInput{
		ChannelID: fixture.channel.ID, DefaultCollectionID: &blogCollection.ID,
		DefaultVisibility: &visibility, DefaultPublishStatus: &publishStatus, AutoplayEnabled: &autoplay,
	})
	if err != nil {
		t.Fatalf("save settings: %v", err)
	}
	if saved.DefaultVisibility != "subscribers" || saved.DefaultPublishStatus != "draft" || saved.AutoplayEnabled {
		t.Fatalf("unexpected saved blog settings: %#v", saved)
	}
	other, err := fixture.service.GetSettings(fixture.user, ModuleBlog, secondChannel.ID)
	if err != nil {
		t.Fatalf("get second channel settings: %v", err)
	}
	if other.ChannelID != secondChannel.ID || other.DefaultCollectionID != nil || other.DefaultVisibility != "public" || other.DefaultPublishStatus != "published" {
		t.Fatalf("expected independent default settings, got %#v", other)
	}
}
