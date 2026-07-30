package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/sitehandle"
	"atoman/internal/service"
	"gorm.io/gorm"
)

func TestUserBootstrapCreatesValidUniqueSlugsForNonASCIIUsernames(t *testing.T) {
	db := setupOwnerBootstrapTestDB(t)
	users := []model.User{
		{Username: "张三", Email: "zhang-san@example.com", Password: "hash", IsActive: true},
		{Username: "李四", Email: "li-si@example.com", Password: "hash", IsActive: true},
	}
	for i := range users {
		if err := db.Create(&users[i]).Error; err != nil {
			t.Fatalf("create user %q: %v", users[i].Username, err)
		}
		if err := service.NewUserBootstrapService(db).EnsureDefaults(users[i].UUID, users[i].Username); err != nil {
			t.Fatalf("EnsureDefaults for %q: %v", users[i].Username, err)
		}
	}

	var channels []model.Channel
	if err := db.Order("created_at ASC, id ASC").Find(&channels).Error; err != nil {
		t.Fatalf("find channels: %v", err)
	}
	if len(channels) != len(users) {
		t.Fatalf("channel count = %d, want %d", len(channels), len(users))
	}
	if channels[0].Slug == channels[1].Slug {
		t.Fatalf("channel slugs must be unique, got %q", channels[0].Slug)
	}
	for i, channel := range channels {
		sum := sha256.Sum256([]byte(users[i].Username))
		wantSlug := "channel-" + hex.EncodeToString(sum[:])[:10]
		if channel.Slug != wantSlug {
			t.Fatalf("channel slug for %q = %q, want stable hashed fallback %q", users[i].Username, channel.Slug, wantSlug)
		}
		if _, err := sitehandle.Normalize(channel.Slug); err != nil {
			t.Fatalf("channel slug %q is invalid: %v", channel.Slug, err)
		}
		if err := sitehandle.NewService(db).ValidateChannelSlugAvailable(context.Background(), channel.Slug, &channel.ID); err != nil {
			t.Fatalf("channel slug %q is unavailable: %v", channel.Slug, err)
		}
	}
}

func TestUserBootstrapCreatesValidSlugForFortyCharacterASCIIUsername(t *testing.T) {
	db := setupOwnerBootstrapTestDB(t)
	username := strings.Repeat("a", 40)
	user := model.User{Username: username, Email: "long-ascii@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	if err := service.NewUserBootstrapService(db).EnsureDefaults(user.UUID, user.Username); err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}

	assertUserChannelSlugIsValid(t, db, user.UUID)
}

func TestUserBootstrapCreatesValidSlugForOneCharacterUsername(t *testing.T) {
	db := setupOwnerBootstrapTestDB(t)
	user := model.User{Username: "a", Email: "one-character@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	if err := service.NewUserBootstrapService(db).EnsureDefaults(user.UUID, user.Username); err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}

	assertUserChannelSlugIsValid(t, db, user.UUID)
}

func TestUserBootstrapCreatesValidSlugWhenLongNameNeedsCollisionSuffix(t *testing.T) {
	db := setupOwnerBootstrapTestDB(t)
	sharedPrefix := strings.Repeat("a", 30)
	users := []model.User{
		{Username: sharedPrefix + "first", Email: "long-first@example.com", Password: "hash", IsActive: true},
		{Username: sharedPrefix + "second", Email: "long-second@example.com", Password: "hash", IsActive: true},
		{Username: sharedPrefix + "third", Email: "long-third@example.com", Password: "hash", IsActive: true},
	}
	for i := range users {
		if err := db.Create(&users[i]).Error; err != nil {
			t.Fatalf("create user %q: %v", users[i].Username, err)
		}
		if err := service.NewUserBootstrapService(db).EnsureDefaults(users[i].UUID, users[i].Username); err != nil {
			t.Fatalf("EnsureDefaults for %q: %v", users[i].Username, err)
		}
	}

	var channels []model.Channel
	if err := db.Order("created_at ASC, id ASC").Find(&channels).Error; err != nil {
		t.Fatalf("find channels: %v", err)
	}
	if len(channels) != len(users) {
		t.Fatalf("channel count = %d, want %d", len(channels), len(users))
	}
	if !strings.HasSuffix(channels[1].Slug, "-2") {
		t.Fatalf("second channel slug = %q, want collision suffix -2", channels[1].Slug)
	}
	if !strings.HasSuffix(channels[2].Slug, "-3") {
		t.Fatalf("third channel slug = %q, want collision suffix -3", channels[2].Slug)
	}
	seen := make(map[string]struct{}, len(channels))
	for _, channel := range channels {
		if _, exists := seen[channel.Slug]; exists {
			t.Fatalf("channel slugs must be unique, duplicate %q", channel.Slug)
		}
		seen[channel.Slug] = struct{}{}
		if _, err := sitehandle.Normalize(channel.Slug); err != nil {
			t.Fatalf("channel slug %q is invalid: %v", channel.Slug, err)
		}
	}
}

func assertUserChannelSlugIsValid(t *testing.T, db *gorm.DB, userID any) {
	t.Helper()
	var channel model.Channel
	if err := db.Where("user_id = ?", userID).First(&channel).Error; err != nil {
		t.Fatalf("find channel: %v", err)
	}
	if _, err := sitehandle.Normalize(channel.Slug); err != nil {
		t.Fatalf("channel slug %q is invalid: %v", channel.Slug, err)
	}
}
