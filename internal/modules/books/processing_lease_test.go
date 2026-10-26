package books

import (
	"context"
	"errors"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPublicBookScanOldLeaseCannotCreateRevision(t *testing.T) {
	service, user, work, store := newBookResourceTestService(t)
	ctx := context.Background()
	asset, err := service.CreateBookResource(ctx, user, uuid.MustParse(work.ID), CreateBookResourceInput{Language: "zh", Format: "txt"})
	require.NoError(t, err)
	session := queueBookTestFile(t, service, user, store, asset, "lease.txt", "text/plain", []byte("public lease test"))
	require.NoError(t, service.db.Model(&model.BookUploadSession{}).Where("id = ?", session.ID).
		Updates(map[string]any{"status": "scanning", "scan_lease_token": "new-lease"}).Error)
	// A late scanner that no longer owns the upload must not create a revision.
	require.NoError(t, service.publishBookResourceUpload(ctx, model.BookUploadSession{
		Base: model.Base{ID: session.ID}, AssetID: asset.ID, UserID: user.ID,
		ObjectKey: store.lastKey, OriginalFilename: "lease.txt",
	}, "0123456789abcdef", "old-lease"))
	var count int64
	require.NoError(t, service.db.Model(&model.BookAssetRevision{}).Where("asset_id = ?", asset.ID).Count(&count).Error)
	require.Zero(t, count)
	var persisted model.BookUploadSession
	require.NoError(t, service.db.First(&persisted, "id = ?", session.ID).Error)
	require.Equal(t, "scanning", persisted.Status)
	require.Equal(t, "new-lease", persisted.ScanLeaseToken)
}

func TestPrivateBookScanOldLeaseCannotFailNewWorker(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.UserBookImport{}, &model.UserBookAsset{})
	importID, assetID := uuid.New(), uuid.New()
	require.NoError(t, db.Create(&model.UserBookImport{
		Base: model.Base{ID: importID}, UserID: uuid.New(), Title: "private lease",
		OriginalFilename: "lease.epub", Format: "epub", ContentType: "application/epub+zip",
		ObjectKey: "books/private/lease.epub", Status: model.BookImportStatusScanning,
	}).Error)
	require.NoError(t, db.Create(&model.UserBookAsset{
		Base: model.Base{ID: assetID}, ImportID: importID, UserID: uuid.New(), OriginalFilename: "lease.epub",
		Format: "epub", ContentType: "application/epub+zip", ObjectKey: "books/private/lease.epub",
		ProcessingStatus: model.BookAssetStatusProcessing, ProcessingToken: "new-worker",
	}).Error)
	svc := NewService(db)
	require.NoError(t, svc.markBookAssetFailed(context.Background(), assetID, "expired-worker", errors.New("late scan failed")))
	var asset model.UserBookAsset
	require.NoError(t, db.First(&asset, "id = ?", assetID).Error)
	require.Equal(t, model.BookAssetStatusProcessing, asset.ProcessingStatus)
	require.Equal(t, "new-worker", asset.ProcessingToken)
	var importRow model.UserBookImport
	require.NoError(t, db.First(&importRow, "id = ?", importID).Error)
	require.Equal(t, model.BookImportStatusScanning, importRow.Status)
}

func TestPrivateBookScanOldLeaseCannotPublishPublicCopy(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.UserBookImport{}, &model.UserBookAsset{}, &model.PublishedBookAsset{})
	importID, assetID := uuid.New(), uuid.New()
	require.NoError(t, db.Create(&model.UserBookImport{
		Base: model.Base{ID: importID}, UserID: uuid.New(), Title: "private source",
		OriginalFilename: "scan.epub", Format: "epub", ContentType: "application/epub+zip",
		ObjectKey: "books/private/scan.epub", Status: model.BookImportStatusScanning,
	}).Error)
	require.NoError(t, db.Create(&model.UserBookAsset{
		Base: model.Base{ID: assetID}, ImportID: importID, UserID: uuid.New(), OriginalFilename: "scan.epub",
		Format: "epub", ContentType: "application/epub+zip",
		ObjectKey: "books/private/scan.epub", SHA256: "abc",
		ProcessingStatus: model.BookAssetStatusProcessing, ProcessingToken: "new-lease",
	}).Error)
	store := &fakeBookUploadStore{objects: map[string][]byte{"books/private/scan.epub": []byte("original")}}
	svc := NewService(db).WithBookUploadStore(store)
	source := model.UserBookAsset{
		Base: model.Base{ID: assetID}, ImportID: importID, ObjectKey: "books/private/scan.epub",
		Format: "epub", ContentType: "application/epub+zip", SHA256: "abc",
		ProcessingStatus: model.BookAssetStatusProcessing, ProcessingToken: "expired-lease",
	}
	require.NoError(t, svc.publishScannedBookAsset(context.Background(), source))
	var published int64
	require.NoError(t, db.Model(&model.PublishedBookAsset{}).Where("source_asset_id = ?", assetID).Count(&published).Error)
	require.Zero(t, published, "expired scanner must never create a public copy")
	require.Len(t, store.objects, 1, "expired scanner must not copy or remove storage objects")
}
