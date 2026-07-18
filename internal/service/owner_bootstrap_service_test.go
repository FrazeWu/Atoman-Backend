package service_test

import (
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/service"
	"atoman/internal/testdb"
	"gorm.io/gorm"
)

func setupOwnerBootstrapTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.UserSettings{},
		&model.Channel{},
		&model.Collection{},
		&model.UserStudioState{},
		&model.StudioModuleSettings{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
		&model.BookmarkFolder{},
		&model.Playlist{},
	)
	return db
}

func TestEnsureOwnerCreatesDefaultResources(t *testing.T) {
	db := setupOwnerBootstrapTestDB(t)

	owner, created, err := service.NewOwnerBootstrapService(db).EnsureOwner(service.OwnerBootstrapInput{
		Username: "owner",
		Email:    "owner@example.com",
		Password: "change-me",
	})
	if err != nil {
		t.Fatalf("EnsureOwner error = %v", err)
	}
	if !created {
		t.Fatalf("EnsureOwner created = false, want true")
	}

	var channels []model.Channel
	if err := db.Where("user_id = ?", owner.UUID).Find(&channels).Error; err != nil {
		t.Fatalf("find default channels: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("expected one unified studio channel, got %d", len(channels))
	}
	channel := channels[0]
	var state model.UserStudioState
	if err := db.First(&state, "user_id = ?", owner.UUID).Error; err != nil {
		t.Fatalf("find current studio channel: %v", err)
	}
	if state.ChannelID == nil || *state.ChannelID != channel.ID {
		t.Fatalf("expected current channel %s, got %#v", channel.ID, state.ChannelID)
	}
	for _, contentType := range []string{"blog", "podcast", "video"} {
		var collection model.Collection
		if err := db.Where("channel_id = ? AND content_type = ? AND is_default = ?", channel.ID, contentType, true).First(&collection).Error; err != nil {
			t.Fatalf("find %s default collection: %v", contentType, err)
		}
	}

	var group model.SubscriptionGroup
	if err := db.Where("user_id = ? AND name = ?", owner.UUID, "默认分组").First(&group).Error; err != nil {
		t.Fatalf("find default subscription group: %v", err)
	}

	var source model.FeedSource
	if err := db.Where("source_type = ? AND source_id = ?", "internal_user", owner.UUID).First(&source).Error; err != nil {
		t.Fatalf("find internal user feed source: %v", err)
	}

	var subscription model.Subscription
	if err := db.Where("user_id = ? AND feed_source_id = ?", owner.UUID, source.ID).First(&subscription).Error; err != nil {
		t.Fatalf("find self subscription: %v", err)
	}

	var folder model.BookmarkFolder
	if err := db.Where("user_id = ? AND name = ?", owner.UUID, "默认收藏夹").First(&folder).Error; err != nil {
		t.Fatalf("find default bookmark folder: %v", err)
	}

	var playlists []model.Playlist
	if err := db.Where("user_id = ?", owner.UUID).Find(&playlists).Error; err != nil {
		t.Fatalf("find playlists: %v", err)
	}
	if len(playlists) != 1 || !playlists[0].IsFavorite || playlists[0].IsPublic {
		t.Fatalf("expected one private favorite playlist, got %#v", playlists)
	}
}

func TestEnsureOwnerRejectsUsernameAndEmailOwnedByDifferentUsers(t *testing.T) {
	db := setupOwnerBootstrapTestDB(t)

	usernameUser := model.User{
		Username: "owner",
		Email:    "other-owner@example.com",
		Password: "old-password",
		Role:     "user",
		IsActive: true,
	}
	if err := db.Create(&usernameUser).Error; err != nil {
		t.Fatalf("create username user: %v", err)
	}

	emailUser := model.User{
		Username: "other",
		Email:    "owner@example.com",
		Password: "old-password",
		Role:     "user",
		IsActive: true,
	}
	if err := db.Create(&emailUser).Error; err != nil {
		t.Fatalf("create email user: %v", err)
	}

	_, created, err := service.NewOwnerBootstrapService(db).EnsureOwner(service.OwnerBootstrapInput{
		Username: "owner",
		Email:    "owner@example.com",
		Password: "change-me",
	})
	if err == nil {
		t.Fatalf("EnsureOwner error = nil, want conflict")
	}
	if created {
		t.Fatalf("EnsureOwner created = true, want false")
	}

	var users []model.User
	if err := db.Order("username").Find(&users).Error; err != nil {
		t.Fatalf("find users: %v", err)
	}
	for _, user := range users {
		if user.Role == "owner" {
			t.Fatalf("user %s role = owner, want no promotion on conflict", user.Username)
		}
	}
}

func TestEnsureOwnerRejectsPartialUsernameCollision(t *testing.T) {
	db := setupOwnerBootstrapTestDB(t)

	existing := model.User{
		Username: "owner",
		Email:    "existing@example.com",
		Password: "old-password",
		Role:     "user",
		IsActive: false,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create existing user: %v", err)
	}
	if err := db.Model(&existing).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate existing user: %v", err)
	}

	_, created, err := service.NewOwnerBootstrapService(db).EnsureOwner(service.OwnerBootstrapInput{
		Username: "owner",
		Email:    "owner@example.com",
		Password: "change-me",
	})
	if err == nil {
		t.Fatalf("EnsureOwner error = nil, want conflict")
	}
	if created {
		t.Fatalf("EnsureOwner created = true, want false")
	}

	var got model.User
	if err := db.Where("uuid = ?", existing.UUID).First(&got).Error; err != nil {
		t.Fatalf("find existing user: %v", err)
	}
	if got.Role == "owner" {
		t.Fatalf("existing role = owner, want unchanged")
	}
	if got.Email != "existing@example.com" {
		t.Fatalf("existing email = %q, want existing@example.com", got.Email)
	}
	if got.IsActive {
		t.Fatalf("existing is_active = true, want unchanged false")
	}

	var count int64
	if err := db.Model(&model.User{}).Where("email = ?", "owner@example.com").Count(&count).Error; err != nil {
		t.Fatalf("count target email users: %v", err)
	}
	if count != 0 {
		t.Fatalf("target email user count = %d, want 0", count)
	}
}

func TestEnsureOwnerRejectsPartialEmailCollision(t *testing.T) {
	db := setupOwnerBootstrapTestDB(t)

	existing := model.User{
		Username: "existing",
		Email:    "owner@example.com",
		Password: "old-password",
		Role:     "user",
		IsActive: false,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create existing user: %v", err)
	}
	if err := db.Model(&existing).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate existing user: %v", err)
	}

	_, created, err := service.NewOwnerBootstrapService(db).EnsureOwner(service.OwnerBootstrapInput{
		Username: "owner",
		Email:    "owner@example.com",
		Password: "change-me",
	})
	if err == nil {
		t.Fatalf("EnsureOwner error = nil, want conflict")
	}
	if created {
		t.Fatalf("EnsureOwner created = true, want false")
	}

	var got model.User
	if err := db.Where("uuid = ?", existing.UUID).First(&got).Error; err != nil {
		t.Fatalf("find existing user: %v", err)
	}
	if got.Role == "owner" {
		t.Fatalf("existing role = owner, want unchanged")
	}
	if got.Username != "existing" {
		t.Fatalf("existing username = %q, want existing", got.Username)
	}
	if got.IsActive {
		t.Fatalf("existing is_active = true, want unchanged false")
	}

	var count int64
	if err := db.Model(&model.User{}).Where("username = ?", "owner").Count(&count).Error; err != nil {
		t.Fatalf("count target username users: %v", err)
	}
	if count != 0 {
		t.Fatalf("target username user count = %d, want 0", count)
	}
}

func TestEnsureOwnerUpdatesOnlyExactUsernameEmailMatch(t *testing.T) {
	db := setupOwnerBootstrapTestDB(t)

	existing := model.User{
		Username: "owner",
		Email:    "owner@example.com",
		Password: "old-password",
		Role:     "user",
		IsActive: false,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create existing user: %v", err)
	}
	if err := db.Model(&existing).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate existing user: %v", err)
	}

	owner, created, err := service.NewOwnerBootstrapService(db).EnsureOwner(service.OwnerBootstrapInput{
		Username: "owner",
		Email:    "owner@example.com",
		Password: "change-me",
	})
	if err != nil {
		t.Fatalf("EnsureOwner error = %v", err)
	}
	if created {
		t.Fatalf("EnsureOwner created = true, want false")
	}
	if owner.UUID != existing.UUID {
		t.Fatalf("owner UUID = %s, want %s", owner.UUID, existing.UUID)
	}
	if owner.Role != "owner" {
		t.Fatalf("owner role = %q, want owner", owner.Role)
	}
	if !owner.IsActive {
		t.Fatalf("owner is_active = false, want true")
	}
	if owner.Password == "old-password" || !strings.HasPrefix(owner.Password, "$2") {
		t.Fatalf("owner password was not replaced with bcrypt hash")
	}
}
