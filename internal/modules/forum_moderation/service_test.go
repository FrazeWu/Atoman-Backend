package forum_moderation

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newForumModerationTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.ForumCategory{}, &model.ForumModeratorAssignment{})
	admin := createModerationUser(t, db, "admin", authctx.RoleAdmin)
	return NewService(db), db, admin
}

func createModerationUser(t *testing.T, db *gorm.DB, username, role string) authctx.CurrentUser {
	t.Helper()
	user := model.User{Username: username, Email: username + "@example.com", Password: "hash", Role: role, IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	return authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
}
