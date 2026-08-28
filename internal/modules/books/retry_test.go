package books

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRetryBookImportResetsOrdinaryFailuresButNotQuarantine(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.UserBookImport{}, &model.UserBookAsset{})
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	failedImport := model.UserBookImport{UserID: owner.ID, Title: "failed", OriginalFilename: "failed.txt", Format: "txt", Status: model.BookImportStatusFailed, ObjectKey: "private/failed.txt"}
	require.NoError(t, db.Create(&failedImport).Error)
	failedAsset := model.UserBookAsset{ImportID: failedImport.ID, UserID: owner.ID, OriginalFilename: "failed.txt", Format: "txt", ContentType: "text/plain", ObjectKey: failedImport.ObjectKey, ProcessingStatus: model.BookAssetStatusFailed, ScanStatus: "failed"}
	require.NoError(t, db.Create(&failedAsset).Error)

	retried, err := NewService(db).RetryBookImport(owner, failedImport.ID)
	require.NoError(t, err)
	require.Equal(t, model.BookImportStatusScanning, retried.Status)
	require.Equal(t, model.BookAssetStatusScanning, retried.ProcessingStatus)

	quarantinedImport := model.UserBookImport{UserID: owner.ID, Title: "quarantined", OriginalFilename: "bad.txt", Format: "txt", Status: model.BookImportStatusFailed, ObjectKey: "private/bad.txt"}
	require.NoError(t, db.Create(&quarantinedImport).Error)
	require.NoError(t, db.Create(&model.UserBookAsset{ImportID: quarantinedImport.ID, UserID: owner.ID, OriginalFilename: "bad.txt", Format: "txt", ContentType: "text/plain", ObjectKey: quarantinedImport.ObjectKey, ProcessingStatus: model.BookAssetStatusQuarantined, ScanStatus: "infected"}).Error)
	_, err = NewService(db).RetryBookImport(owner, quarantinedImport.ID)
	require.Error(t, err)
}
