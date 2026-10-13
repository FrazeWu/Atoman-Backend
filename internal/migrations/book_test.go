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
		"book_publishers",
		"book_producers",
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
	var publisherCount, producerCount int64
	require.NoError(t, db.Model(&model.BookPublisher{}).Where("deleted_at IS NULL").Count(&publisherCount).Error)
	require.NoError(t, db.Model(&model.BookProducer{}).Where("deleted_at IS NULL").Count(&producerCount).Error)
	require.GreaterOrEqual(t, publisherCount, int64(30))
	require.GreaterOrEqual(t, producerCount, int64(10))
	seededPublisherCount, seededProducerCount := publisherCount, producerCount
	require.NoError(t, RunBooksMigration(db))
	require.NoError(t, db.Model(&model.BookPublisher{}).Where("deleted_at IS NULL").Count(&publisherCount).Error)
	require.NoError(t, db.Model(&model.BookProducer{}).Where("deleted_at IS NULL").Count(&producerCount).Error)
	require.Equal(t, seededPublisherCount, publisherCount)
	require.Equal(t, seededProducerCount, producerCount)

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

func TestBackfillBookPublishersBatchesAndReusesPublishers(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookPublisher{}, &model.BookEdition{})

	existing := model.BookPublisher{Name: "Existing Publisher", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&existing).Error)
	editions := []model.BookEdition{
		{WorkID: uuid.New(), Publisher: "New Publisher"},
		{WorkID: uuid.New(), Publisher: " New Publisher "},
		{WorkID: uuid.New(), Publisher: "Existing Publisher"},
		{WorkID: uuid.New(), Publisher: "  "},
	}
	require.NoError(t, db.Create(&editions).Error)

	require.NoError(t, backfillBookPublishers(db))
	require.NoError(t, backfillBookPublishers(db))

	var newPublisherCount int64
	require.NoError(t, db.Model(&model.BookPublisher{}).Where("name = ? AND deleted_at IS NULL", "New Publisher").Count(&newPublisherCount).Error)
	require.EqualValues(t, 1, newPublisherCount)
	for index, edition := range editions {
		var stored model.BookEdition
		require.NoError(t, db.First(&stored, "id = ?", edition.ID).Error)
		if index == len(editions)-1 {
			require.Nil(t, stored.PublisherID)
			continue
		}
		require.NotNil(t, stored.PublisherID)
		if index == 2 {
			require.Equal(t, existing.ID, *stored.PublisherID)
		}
	}
}
