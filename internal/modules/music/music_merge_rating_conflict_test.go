package music

import (
	"atoman/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestMergeRatingsKeepsTargetRatingWhenUserRatedBothEntries(t *testing.T) {
	_, db, user := newMusicTestService(t)
	sourceSong := model.Song{Title: "source song", AudioURL: "/source.mp3"}
	targetSong := model.Song{Title: "target song", AudioURL: "/target.mp3"}
	sourceAlbum := model.Album{Title: "source album"}
	targetAlbum := model.Album{Title: "target album"}
	for _, entity := range []any{&sourceSong, &targetSong, &sourceAlbum, &targetAlbum} {
		require.NoError(t, db.Create(entity).Error)
	}
	for _, rating := range []any{
		&model.SongRating{UserID: user.ID, SongID: sourceSong.ID, Score: 4},
		&model.SongRating{UserID: user.ID, SongID: targetSong.ID, Score: 9},
		&model.AlbumRating{UserID: user.ID, AlbumID: sourceAlbum.ID, Score: 3},
		&model.AlbumRating{UserID: user.ID, AlbumID: targetAlbum.ID, Score: 8},
	} {
		require.NoError(t, db.Create(rating).Error)
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := mergeSongRatings(tx, sourceSong.ID, targetSong.ID); err != nil {
			return err
		}
		return mergeAlbumRatings(tx, sourceAlbum.ID, targetAlbum.ID)
	}))
	var songs []model.SongRating
	require.NoError(t, db.Where("user_id = ? AND song_id = ?", user.ID, targetSong.ID).Find(&songs).Error)
	require.Len(t, songs, 1)
	require.Equal(t, 9, songs[0].Score)
	var albums []model.AlbumRating
	require.NoError(t, db.Where("user_id = ? AND album_id = ?", user.ID, targetAlbum.ID).Find(&albums).Error)
	require.Len(t, albums, 1)
	require.Equal(t, 8, albums[0].Score)
	var liveSource int64
	require.NoError(t, db.Model(&model.SongRating{}).Where("user_id = ? AND song_id = ?", user.ID, sourceSong.ID).Count(&liveSource).Error)
	require.Zero(t, liveSource)
}
