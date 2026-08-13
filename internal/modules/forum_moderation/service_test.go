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
	testdb.Migrate(t, db, &model.User{}, &model.ForumCategory{}, &model.ForumTopic{}, &model.ForumModeratorAssignment{}, &model.ForumUserModerationAction{}, &model.Notification{}, &model.AuditLog{})
	admin := createModerationUser(t, db, "admin", authctx.RoleAdmin)
	return NewService(db), db, admin
}

func TestFeatureTopicDoesNotChangePinnedState(t *testing.T) {
	service, db, admin := newForumModerationTestService(t)
	category := model.ForumCategory{Name: "Feature category", Description: "feature"}
	require.NoError(t, db.Create(&category).Error)
	topic := model.ForumTopic{UserID: admin.ID, CategoryID: category.ID, Title: "Feature topic", Content: "body"}
	require.NoError(t, db.Create(&topic).Error)

	featured, err := service.FeatureTopic(admin, topic.ID)
	require.NoError(t, err)
	require.True(t, featured.Featured)
	require.False(t, featured.Pinned)

	pinned, err := service.PinTopic(admin, topic.ID)
	require.NoError(t, err)
	require.True(t, pinned.Featured)
	require.True(t, pinned.Pinned)

	unfeatured, err := service.UnfeatureTopic(admin, topic.ID)
	require.NoError(t, err)
	require.False(t, unfeatured.Featured)
	require.True(t, unfeatured.Pinned)
}

func createModerationUser(t *testing.T, db *gorm.DB, username, role string) authctx.CurrentUser {
	t.Helper()
	user := model.User{Username: username, Email: username + "@example.com", Password: "hash", Role: role, IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	return authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
}
