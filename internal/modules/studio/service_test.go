package studio

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
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

func TestDeleteChannelRejectsActiveVideoProcessingJob(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Collection{}, &model.Post{}, &model.Video{}, &model.VideoProcessingJob{}, &model.ContentEntry{})
	user := model.User{Username: "channel-delete-owner", Email: "channel-delete-owner@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{UserID: &user.UUID, Name: "Processing Channel", Slug: "processing-channel"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	video := model.Video{UserID: user.UUID, ChannelID: &channel.ID, Title: "Removed video", VideoURL: "https://example.com/video.mp4"}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	job := model.VideoProcessingJob{VideoID: video.ID, Status: "processing"}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}

	err := NewService(db).DeleteChannel(authctx.CurrentUser{ID: user.UUID, Username: user.Username}, channel.ID)
	if app := apperr.FromError(err); app == nil || app.HTTPStatus != 409 || app.Code != "studio.channel_processing_in_progress" {
		t.Fatalf("expected active processing conflict, got %v", err)
	}
	var persisted model.Channel
	if err := db.First(&persisted, "id = ?", channel.ID).Error; err != nil {
		t.Fatalf("channel must remain after rejected delete: %v", err)
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
