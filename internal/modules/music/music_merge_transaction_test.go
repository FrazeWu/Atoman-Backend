package music

import (
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"errors"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestAlbumRatingMergeRollsBackOnDownstreamFailure(t *testing.T) {
	_, db, user := newMusicTestService(t)
	source := model.Album{Title: "rollback source"}
	target := model.Album{Title: "rollback target"}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&target).Error)
	rating := model.AlbumRating{UserID: user.ID, AlbumID: source.ID, Score: 7}
	require.NoError(t, db.Create(&rating).Error)
	injected := errors.New("injected downstream failure")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := mergeAlbumRatings(tx, source.ID, target.ID); err != nil {
			return err
		}
		return injected
	})
	require.ErrorIs(t, err, injected)
	var original model.AlbumRating
	require.NoError(t, db.Where("user_id = ? AND album_id = ?", user.ID, source.ID).First(&original).Error)
	require.Equal(t, 7, original.Score)
	var migrated int64
	require.NoError(t, db.Model(&model.AlbumRating{}).Where("album_id = ?", target.ID).Count(&migrated).Error)
	require.Zero(t, migrated)
}

func TestMergeAlbumsRepeatedRequestDoesNotReapply(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	source := model.Album{Title: "repeat source", LifecycleStatus: model.MusicLifecycleActive}
	target := model.Album{Title: "repeat target", LifecycleStatus: model.MusicLifecycleActive}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&target).Error)
	admin := authctx.CurrentUser{ID: user.ID, Role: authctx.RoleAdmin}
	require.NoError(t, svc.MergeAlbums(admin, target.ID, source.ID, nil))
	require.Error(t, svc.MergeAlbums(admin, target.ID, source.ID, nil))
	var result model.Album
	require.NoError(t, db.First(&result, "id = ?", source.ID).Error)
	require.Equal(t, model.MusicLifecycleMerged, result.LifecycleStatus)
	require.NotNil(t, result.RedirectTo)
	require.Equal(t, target.ID, *result.RedirectTo)
}
