package books

import (
	"context"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookShelfAndContinueReadingStayPrivateAndFilterable(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookSource{}, &model.BookRating{}, &model.UserBookShelf{}, &model.UserBookImport{}, &model.UserBookAsset{}, &model.UserBookReadingState{})
	service := NewService(db)
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	other := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	work := model.BookWork{Title: "Shelf work", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&work).Error)

	saved, err := service.SaveBookShelf(owner, work.ID, SaveBookShelfInput{Status: model.BookShelfStatusReading, Note: "current book"})
	require.NoError(t, err)
	require.Equal(t, model.BookShelfStatusReading, saved.Status)
	require.Equal(t, "Shelf work", saved.Work.Title)

	items, total, err := service.ListBookShelf(context.Background(), owner, model.BookShelfStatusReading, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	_, otherTotal, err := service.ListBookShelf(context.Background(), other, "", 20, 0)
	require.NoError(t, err)
	require.Zero(t, otherTotal)

	importID := uuid.New()
	assetID := uuid.New()
	lastRead := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, db.Create(&model.UserBookImport{
		Base: model.Base{ID: importID}, UserID: owner.ID, Title: "Private title", OriginalFilename: "private.txt", Format: "txt", ContentType: "text/plain", SizeBytes: 12, Status: model.BookImportStatusMetadataReady,
	}).Error)
	require.NoError(t, db.Create(&model.UserBookAsset{
		Base: model.Base{ID: assetID}, ImportID: importID, UserID: owner.ID, OriginalFilename: "private.txt", Format: "txt", ContentType: "text/plain", SizeBytes: 12, ProcessingStatus: model.BookAssetStatusPrivateAvailable,
	}).Error)
	require.NoError(t, db.Create(&model.UserBookReadingState{
		UserID: owner.ID, AssetID: assetID, ReadingPercent: 0.4, LastReadAt: &lastRead,
	}).Error)

	continueItems, err := service.ListContinueReading(context.Background(), owner, 20)
	require.NoError(t, err)
	require.Len(t, continueItems, 1)
	require.Equal(t, "Private title", continueItems[0].Title)
	require.Equal(t, assetID.String(), continueItems[0].AssetID)

	require.NoError(t, service.DeleteBookShelf(owner, work.ID))
	items, total, err = service.ListBookShelf(context.Background(), owner, "", 20, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, items)
}
