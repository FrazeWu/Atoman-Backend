package notification

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSavePreferencesRejectsMismatchedEventCategory(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.NotificationPreference{})
	user := model.User{Username: "category-test", Email: "category-test@example.test", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	svc := NewService(db)
	viewer := authctx.CurrentUser{ID: user.UUID}
	_, err := svc.SavePreferences(viewer, SavePreferencesInput{Items: []PreferenceInput{
		{Category: "reply", EventType: "comment_like", Enabled: false},
	}})
	require.ErrorContains(t, err, "Notification preference is invalid")

	var count int64
	require.NoError(t, db.Model(&model.NotificationPreference{}).Where("user_id = ?", user.UUID).Count(&count).Error)
	require.Zero(t, count, "an invalid category must never persist")
}

func TestSavePreferencesAllowsCanonicalCategories(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.NotificationPreference{})
	user := model.User{Username: "category-valid", Email: "category-valid@example.test", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	items, err := NewService(db).SavePreferences(authctx.CurrentUser{ID: user.UUID}, SavePreferencesInput{Items: []PreferenceInput{
		{Category: "like", EventType: "comment_like", Enabled: false},
		{Category: "system", EventType: "site_announcement", Enabled: true},
	}})
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.NotEqual(t, uuid.Nil, items[0].UserID)
}
