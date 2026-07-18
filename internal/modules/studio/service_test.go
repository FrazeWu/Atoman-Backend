package studio

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestValidateContentScopeUsesCollectionModuleInsteadOfChannelType(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Collection{})
	user := model.User{Username: "scope-owner", Email: "scope-owner@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{UserID: &user.UUID, Name: "Shared Channel", Slug: "shared-channel"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	podcastCollection := model.Collection{ChannelID: channel.ID, ContentType: string(ModulePodcast), Name: "Episodes"}
	if err := db.Create(&podcastCollection).Error; err != nil {
		t.Fatal(err)
	}

	if err := NewService(db).ValidateContentScope(user.UUID, channel.ID, ModulePodcast, []uuid.UUID{podcastCollection.ID}, true); err != nil {
		t.Fatalf("expected shared channel with podcast collection to be valid: %v", err)
	}
}

func TestValidateContentScopeRejectsCollectionFromAnotherModule(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Collection{})
	user := model.User{Username: "module-owner", Email: "module-owner@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{UserID: &user.UUID, Name: "Module Channel", Slug: "module-channel"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	videoCollection := model.Collection{ChannelID: channel.ID, ContentType: string(ModuleVideo), Name: "Videos"}
	if err := db.Create(&videoCollection).Error; err != nil {
		t.Fatal(err)
	}

	err := NewService(db).ValidateContentScope(user.UUID, channel.ID, ModuleBlog, []uuid.UUID{videoCollection.ID}, true)
	if app := apperr.FromError(err); app == nil || app.HTTPStatus != 400 {
		t.Fatalf("expected module mismatch 400, got %v", err)
	}
}

func TestValidateContentScopeAllowsEmptyDraftAndRejectsEmptyPublish(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Collection{})
	user := model.User{Username: "draft-owner", Email: "draft-owner@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{UserID: &user.UUID, Name: "Draft Channel", Slug: "draft-channel"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	if err := service.ValidateContentScope(user.UUID, channel.ID, ModuleBlog, nil, false); err != nil {
		t.Fatalf("expected draft without collection to be valid: %v", err)
	}
	if app := apperr.FromError(service.ValidateContentScope(user.UUID, channel.ID, ModuleBlog, nil, true)); app == nil || app.HTTPStatus != 400 {
		t.Fatalf("expected publish without collection to return 400")
	}
}
