package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRunChannelDefaultSelectionMigrationBackfillsContentTypeAndSelection(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{})

	user := model.User{
		Username: "migration-user",
		Email:    "migration-user@example.com",
		Password: "hash",
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	channel := model.Channel{
		UserID:    &user.UUID,
		Name:      "Legacy Default",
		Slug:      "legacy-default",
		IsDefault: true,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := db.Model(&model.Channel{}).Where("id = ?", channel.ID).Update("content_type", "").Error; err != nil {
		t.Fatalf("blank channel content type: %v", err)
	}

	if err := RunChannelDefaultSelectionMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	var updated model.Channel
	if err := db.First(&updated, "id = ?", channel.ID).Error; err != nil {
		t.Fatalf("reload channel: %v", err)
	}
	if updated.ContentType != model.ChannelContentTypeBlog {
		t.Fatalf("expected content type %q, got %q", model.ChannelContentTypeBlog, updated.ContentType)
	}

	var selection model.UserDefaultChannel
	if err := db.Where("user_id = ? AND content_type = ?", user.UUID, model.ChannelContentTypeBlog).First(&selection).Error; err != nil {
		t.Fatalf("load selection: %v", err)
	}
	if selection.ChannelID != channel.ID {
		t.Fatalf("expected selected channel %s, got %s", channel.ID, selection.ChannelID)
	}
}

func TestRunChannelDefaultSelectionMigrationPreservesExistingSelection(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.UserDefaultChannel{})

	user := model.User{
		Username: "selection-user",
		Email:    "selection-user@example.com",
		Password: "hash",
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	firstChannel := model.Channel{
		UserID:      &user.UUID,
		Name:        "First Default",
		Slug:        "first-default-" + uuid.NewString()[:8],
		ContentType: model.ChannelContentTypeBlog,
		IsDefault:   true,
	}
	secondChannel := model.Channel{
		UserID:      &user.UUID,
		Name:        "Second Default",
		Slug:        "second-default-" + uuid.NewString()[:8],
		ContentType: model.ChannelContentTypeBlog,
		IsDefault:   true,
	}
	if err := db.Create(&firstChannel).Error; err != nil {
		t.Fatalf("create first channel: %v", err)
	}
	if err := db.Create(&secondChannel).Error; err != nil {
		t.Fatalf("create second channel: %v", err)
	}
	if err := db.Create(&model.UserDefaultChannel{
		UserID:      user.UUID,
		ContentType: model.ChannelContentTypeBlog,
		ChannelID:   secondChannel.ID,
	}).Error; err != nil {
		t.Fatalf("seed selection: %v", err)
	}

	if err := RunChannelDefaultSelectionMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	var selection model.UserDefaultChannel
	if err := db.Where("user_id = ? AND content_type = ?", user.UUID, model.ChannelContentTypeBlog).First(&selection).Error; err != nil {
		t.Fatalf("reload selection: %v", err)
	}
	if selection.ChannelID != secondChannel.ID {
		t.Fatalf("expected selection to stay on %s, got %s", secondChannel.ID, selection.ChannelID)
	}
}
