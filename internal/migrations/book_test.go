package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRunBooksMigrationCreatesIndependentCatalogAndPrivateResourceTables(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{})
	require.NoError(t, RunBooksMigration(db))
	require.NoError(t, RunBooksMigration(db))

	for _, table := range []string{
		"book_works",
		"book_editions",
		"book_people",
		"book_contributions",
		"book_sources",
		"book_edits",
		"user_book_imports",
		"user_book_assets",
		"user_book_reading_states",
		"user_book_shelves",
		"book_publication_requests",
		"book_rights_declarations",
		"published_book_assets",
		"book_ratings",
		"book_reviews",
		"book_post_links",
		"book_publication_appeals"} {
		require.True(t, db.Migrator().HasTable(table), table)
	}

	owner := model.User{UUID: uuid.New(), Username: "book-owner", Email: "book-owner@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	work := model.BookWork{Title: "Public Work", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment, CreatedBy: &owner.UUID}
	require.NoError(t, db.Create(&work).Error)
	require.NoError(t, db.Create(&model.BookRating{UserID: owner.UUID, WorkID: work.ID, Score: 5}).Error)
	require.Error(t, db.Create(&model.BookRating{UserID: owner.UUID, WorkID: work.ID, Score: 4}).Error)
	require.NoError(t, db.Create(&model.BookReview{UserID: owner.UUID, WorkID: work.ID, Content: "值得一读", Visibility: model.BookReviewVisibilityPublic}).Error)
	require.Error(t, db.Create(&model.BookReview{UserID: owner.UUID, WorkID: work.ID, Content: "重复书评", Visibility: model.BookReviewVisibilityPrivate}).Error)
	require.NoError(t, db.Create(&model.UserBookShelf{UserID: owner.UUID, WorkID: work.ID, Status: model.BookShelfStatusReading}).Error)
	require.Error(t, db.Create(&model.UserBookShelf{UserID: owner.UUID, WorkID: work.ID, Status: model.BookShelfStatusWantToRead}).Error)
	require.NoError(t, db.Create(&model.UserBookImport{UserID: owner.UUID, Title: "正在合并", Status: model.BookImportStatusCompleting}).Error)
}
