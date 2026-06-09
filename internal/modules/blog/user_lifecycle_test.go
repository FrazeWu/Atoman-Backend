package blog

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestAnonymizeChannelsForDeactivatedUser(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Post{}, &model.Collection{})

	user := model.User{Username: "deactivated", Email: "deactivated@example.com", Password: "hash", DisplayName: "Deactivated User", IsActive: false}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	existingAnonymous := model.Channel{
		UserID:      nil,
		Name:        "已注销用户",
		Slug:        "anon-existing",
		Description: "existing anonymous channel",
		IsAnonymous: true,
	}
	if err := db.Create(&existingAnonymous).Error; err != nil {
		t.Fatalf("create existing anonymous channel: %v", err)
	}

	channel := model.Channel{
		UserID:      &user.UUID,
		Name:        "Original Channel",
		Slug:        "original-channel",
		Description: "user owned channel",
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel2 := model.Channel{
		UserID:      &user.UUID,
		Name:        "Original Channel 2",
		Slug:        "original-channel-2",
		Description: "second channel",
	}
	if err := db.Create(&channel2).Error; err != nil {
		t.Fatalf("create second channel: %v", err)
	}

	svc := NewService(db)
	if err := svc.AnonymizeUserChannels(user.UUID); err != nil {
		t.Fatalf("anonymize user channels: %v", err)
	}

	var updated []model.Channel
	if err := db.Order("created_at asc").Find(&updated, "id IN ?", []any{channel.ID, channel2.ID}).Error; err != nil {
		t.Fatalf("reload channels: %v", err)
	}
	if len(updated) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(updated))
	}
	if updated[0].UserID != nil || updated[1].UserID != nil {
		t.Fatalf("expected anonymized channels to clear user_id, got %#v", updated)
	}
	if !updated[0].IsAnonymous || !updated[1].IsAnonymous {
		t.Fatalf("expected anonymized channels, got %#v", updated)
	}
	if updated[0].Name != "已注销用户 2" {
		t.Fatalf("expected first anonymized name to avoid existing collision, got %q", updated[0].Name)
	}
	if updated[1].Name != "已注销用户 3" {
		t.Fatalf("expected second anonymized name, got %q", updated[1].Name)
	}
}
