package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type legacyPostRatingForContentMigration struct {
	model.Base
	UserID uuid.UUID `gorm:"type:uuid;not null"`
	PostID uuid.UUID `gorm:"type:uuid;not null"`
	Score  int       `gorm:"not null"`
}

func (legacyPostRatingForContentMigration) TableName() string { return "post_ratings" }

func TestRunBlogRatingContentMigrationBackfillsAndValidatesCanonicalEntries(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &legacyPostRatingForContentMigration{}, &model.User{}, &model.Channel{}, &model.ContentEntry{})

	owner := model.User{Username: "rating-content-migration-owner", Email: "rating-content-migration-owner@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	channel := model.Channel{UserID: &owner.UUID, Name: "Rating migration", Slug: "rating-content-migration"}
	require.NoError(t, db.Create(&channel).Error)
	contentID := uuid.New()
	require.NoError(t, db.Create(&model.ContentEntry{
		Base: model.Base{ID: contentID}, ChannelID: channel.ID, Kind: "blog", Title: "Rated content", Status: "published", Visibility: "public",
	}).Error)
	legacyRating := legacyPostRatingForContentMigration{UserID: uuid.New(), PostID: contentID, Score: 8}
	require.NoError(t, db.Create(&legacyRating).Error)

	require.NoError(t, RunBlogRatingContentMigration(db))
	require.NoError(t, RunBlogRatingContentMigration(db))

	var rating model.PostRating
	require.NoError(t, db.First(&rating, "id = ?", legacyRating.ID).Error)
	require.Equal(t, contentID, rating.ContentID)
	assertIndexExists(t, db, "post_ratings", "idx_post_ratings_user_content")
}

func TestRunBlogRatingContentMigrationFailsWithoutCanonicalBlogEntry(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &legacyPostRatingForContentMigration{}, &model.ContentEntry{})
	require.NoError(t, db.Create(&legacyPostRatingForContentMigration{
		UserID: uuid.New(), PostID: uuid.New(), Score: 8,
	}).Error)

	require.Error(t, RunBlogRatingContentMigration(db))
}

func TestRunBlogRatingContentMigrationSkipsMissingLegacyPostID(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.ContentEntry{}, &model.PostRating{})
	require.False(t, db.Migrator().HasColumn("post_ratings", "post_id"))

	require.NoError(t, RunBlogRatingContentMigration(db))
	require.NoError(t, RunBlogRatingContentMigration(db))
	assertIndexExists(t, db, "post_ratings", "idx_post_ratings_user_content")
}
