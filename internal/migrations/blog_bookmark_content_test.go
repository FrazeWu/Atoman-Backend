package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type legacyBookmarkForContentMigration struct {
	model.Base
	UserID uuid.UUID `gorm:"type:uuid;not null"`
	PostID uuid.UUID `gorm:"type:uuid;not null"`
}

func (legacyBookmarkForContentMigration) TableName() string { return "bookmarks" }

func TestRunBlogBookmarkContentMigrationBackfillsAndValidatesCanonicalEntries(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &legacyBookmarkForContentMigration{}, &model.User{}, &model.Channel{}, &model.ContentEntry{})

	owner := model.User{Username: "bookmark-content-migration-owner", Email: "bookmark-content-migration-owner@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	channel := model.Channel{UserID: &owner.UUID, Name: "Bookmark migration", Slug: "bookmark-content-migration"}
	require.NoError(t, db.Create(&channel).Error)
	contentID := uuid.New()
	require.NoError(t, db.Create(&model.ContentEntry{
		Base: model.Base{ID: contentID}, ChannelID: channel.ID, Kind: "blog", Title: "Bookmarked content", Status: "published", Visibility: "public",
	}).Error)
	legacyBookmark := legacyBookmarkForContentMigration{UserID: uuid.New(), PostID: contentID}
	require.NoError(t, db.Create(&legacyBookmark).Error)

	require.NoError(t, RunBlogBookmarkContentMigration(db))
	require.NoError(t, RunBlogBookmarkContentMigration(db))

	var bookmark model.Bookmark
	require.NoError(t, db.First(&bookmark, "id = ?", legacyBookmark.ID).Error)
	require.Equal(t, contentID, bookmark.ContentID)
	assertIndexExists(t, db, "bookmarks", "idx_bookmarks_user_content")
}

func TestRunBlogBookmarkContentMigrationUsesCanonicalPostMapping(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &legacyBookmarkForContentMigration{}, &model.User{}, &model.Channel{}, &model.ContentEntry{}, &model.ContentPostExtension{})

	owner := model.User{Username: "bookmark-content-mapping-owner", Email: "bookmark-content-mapping-owner@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	channel := model.Channel{UserID: &owner.UUID, Name: "Bookmark mapping", Slug: "bookmark-content-mapping"}
	require.NoError(t, db.Create(&channel).Error)
	postID := uuid.New()
	contentID := uuid.New()
	require.NoError(t, db.Create(&model.ContentEntry{
		Base: model.Base{ID: contentID}, ChannelID: channel.ID, Kind: "blog", Title: "Canonical bookmarked content", Status: "published", Visibility: "public",
	}).Error)
	require.NoError(t, db.Create(&model.ContentPostExtension{ContentID: contentID, PostID: postID}).Error)
	legacyBookmark := legacyBookmarkForContentMigration{UserID: uuid.New(), PostID: postID}
	require.NoError(t, db.Create(&legacyBookmark).Error)

	require.NoError(t, RunBlogBookmarkContentMigration(db))

	var bookmark model.Bookmark
	require.NoError(t, db.First(&bookmark, "id = ?", legacyBookmark.ID).Error)
	require.Equal(t, contentID, bookmark.ContentID)
}

func TestRunBlogBookmarkContentMigrationFailsWithoutCanonicalBlogEntry(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &legacyBookmarkForContentMigration{}, &model.ContentEntry{})
	require.NoError(t, db.Create(&legacyBookmarkForContentMigration{UserID: uuid.New(), PostID: uuid.New()}).Error)

	require.Error(t, RunBlogBookmarkContentMigration(db))
}
