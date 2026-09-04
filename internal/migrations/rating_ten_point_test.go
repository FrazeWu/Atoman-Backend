package migrations

import (
	"testing"

	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type legacySongRatingScale struct {
	ID    uuid.UUID `gorm:"type:uuid;primaryKey"`
	Score int       `gorm:"not null;check:chk_song_ratings_score,score BETWEEN 1 AND 5"`
}

func (legacySongRatingScale) TableName() string { return "song_ratings" }

type legacyAlbumRatingScale struct {
	ID    uuid.UUID `gorm:"type:uuid;primaryKey"`
	Score int       `gorm:"not null;check:chk_album_ratings_score,score BETWEEN 1 AND 5"`
}

func (legacyAlbumRatingScale) TableName() string { return "album_ratings" }

type legacyBookRatingScale struct {
	ID    uuid.UUID `gorm:"type:uuid;primaryKey"`
	Score int       `gorm:"not null;check:chk_book_ratings_score,score BETWEEN 1 AND 5"`
}

func (legacyBookRatingScale) TableName() string { return "book_ratings" }

func TestRunRatingTenPointMigrationConvertsLegacyScoresOnce(t *testing.T) {
	db := testdb.Open(t)
	require.NoError(t, db.AutoMigrate(&legacySongRatingScale{}, &legacyAlbumRatingScale{}, &legacyBookRatingScale{}))

	require.NoError(t, db.Create(&legacySongRatingScale{ID: uuid.New(), Score: 4}).Error)
	require.NoError(t, db.Create(&legacyAlbumRatingScale{ID: uuid.New(), Score: 5}).Error)
	require.NoError(t, db.Create(&legacyBookRatingScale{ID: uuid.New(), Score: 3}).Error)

	require.NoError(t, RunRatingTenPointMigration(db))
	require.NoError(t, RunRatingTenPointMigration(db))

	var song, album, book struct{ Score int }
	require.NoError(t, db.Table("song_ratings").First(&song).Error)
	require.NoError(t, db.Table("album_ratings").First(&album).Error)
	require.NoError(t, db.Table("book_ratings").First(&book).Error)
	require.Equal(t, 8, song.Score)
	require.Equal(t, 10, album.Score)
	require.Equal(t, 6, book.Score)

	require.NoError(t, db.Create(&legacySongRatingScale{ID: uuid.New(), Score: 9}).Error)
	require.Error(t, db.Create(&legacySongRatingScale{ID: uuid.New(), Score: 11}).Error)
}
