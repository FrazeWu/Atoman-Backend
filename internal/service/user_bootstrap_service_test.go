package service_test

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/service"
	"gorm.io/gorm"
)

func TestUserBootstrapCreatesOnlyUserResources(t *testing.T) {
	db := setupOwnerBootstrapTestDB(t)
	user := model.User{Username: "new-user", Email: "new-user@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := service.NewUserBootstrapService(db).EnsureDefaults(user.UUID, user.Username); err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}
	assertCount(t, db, &model.Channel{}, "user_id = ?", user.UUID, 0)
	assertCount(t, db, &model.UserStudioState{}, "user_id = ?", user.UUID, 0)
	assertCount(t, db, &model.ContentCollection{}, "created_by = ?", user.UUID, 0)
	assertCount(t, db, &model.BookmarkFolder{}, "user_id = ? AND name = '默认收藏夹'", user.UUID, 1)
	assertCount(t, db, &model.SubscriptionGroup{}, "user_id = ? AND name = '默认分组'", user.UUID, 1)
	assertCount(t, db, &model.Subscription{}, "user_id = ?", user.UUID, 1)
}

func assertCount(t *testing.T, db *gorm.DB, value any, query string, argument any, want int64) {
	t.Helper()
	var count int64
	if err := db.Model(value).Where(query, argument).Count(&count).Error; err != nil {
		t.Fatalf("count %T: %v", value, err)
	}
	if count != want {
		t.Fatalf("count %T = %d, want %d", value, count, want)
	}
}
