package migrationrunner

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRatingColdStartMigrationIsRepeatable(t *testing.T) {
	db := testdb.Open(t)
	require.NoError(t, Run(db))
	for _, table := range []string{"book_ratings", "song_ratings", "album_ratings", "post_ratings", "feed_item_ratings"} {
		t.Run(table, func(t *testing.T) {
			require.True(t, db.Migrator().HasTable(table))
			var count int64
			require.NoError(t, db.Table(table).Count(&count).Error)
			require.Zero(t, count)
			var constraint string
			require.NoError(t, db.Raw(`SELECT pg_get_constraintdef(oid) FROM pg_constraint
				WHERE conrelid = ?::regclass AND conname = ?`, table, "chk_"+table+"_score").Scan(&constraint).Error)
			require.Contains(t, constraint, "<= 10")
		})
	}
	user := model.User{Username: "first-rater", Email: "first-rater@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	rating := model.PostRating{UserID: user.UUID, ContentID: uuid.New(), Score: 9}
	require.NoError(t, db.Create(&rating).Error)

	// 重跑完整迁移不会再次转换已使用十分制的评分。
	require.NoError(t, Run(db))
	var stored model.PostRating
	require.NoError(t, db.First(&stored, "id = ?", rating.ID).Error)
	require.Equal(t, 9, stored.Score)
	for _, score := range []int{0, 11} {
		require.Error(t, db.Create(&model.PostRating{UserID: user.UUID, ContentID: uuid.New(), Score: score}).Error)
	}
}
