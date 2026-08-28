package books

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLinkBookImportToCatalogRequiresPublicWorkAndPreservesPrivateOwnership(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookSource{}, &model.BookRating{}, &model.UserBookImport{}, &model.UserBookAsset{})
	service := NewService(db)
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	other := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	work := model.BookWork{Title: "Public", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&work).Error)
	importRecord := model.UserBookImport{UserID: owner.ID, Title: "Private title", OriginalFilename: "private.txt", Format: "txt", ContentType: "text/plain", SizeBytes: 10, Status: model.BookImportStatusMetadataReady}
	require.NoError(t, db.Create(&importRecord).Error)

	linked, err := service.LinkBookImportToCatalog(owner, importRecord.ID, LinkBookImportInput{WorkID: work.ID.String()})
	require.NoError(t, err)
	require.Equal(t, work.ID.String(), linked.WorkID)
	require.Equal(t, "Private title", linked.Title)
	_, err = service.LinkBookImportToCatalog(other, importRecord.ID, LinkBookImportInput{WorkID: work.ID.String()})
	require.Error(t, err)
}
