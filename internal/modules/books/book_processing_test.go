package books

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProcessBookAssetsMakesSafeEPUBPrivateAvailable(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.UserBookImport{}, &model.UserBookAsset{})
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	importID := uuid.New()
	assetID := uuid.New()
	key := "books/private/test.epub"
	body := testEPUBBody(t)
	record := model.UserBookImport{
		Base:               model.Base{ID: importID},
		UserID:             owner.ID,
		Title:              "安全 EPUB",
		OriginalFilename:   "safe.epub",
		Format:             "epub",
		ContentType:        "application/epub+zip",
		SizeBytes:          int64(len(body)),
		ObjectKey:          key,
		Status:             model.BookImportStatusScanning,
		MetadataJSON:       "{}",
		CompletedPartsJSON: "[]",
	}
	asset := model.UserBookAsset{
		Base:             model.Base{ID: assetID},
		ImportID:         importID,
		UserID:           owner.ID,
		OriginalFilename: "safe.epub",
		ContentType:      "application/epub+zip",
		Format:           "epub",
		SizeBytes:        int64(len(body)),
		ObjectKey:        key,
		ScanStatus:       "pending",
		ProcessingStatus: model.BookAssetStatusScanning,
	}
	require.NoError(t, db.Create(&record).Error)
	require.NoError(t, db.Create(&asset).Error)
	store := &fakeBookUploadStore{objects: map[string][]byte{key: body}}

	processed, err := NewService(db).WithBookUploadStore(store).ProcessBookAssets(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, processed)
	var processedAsset model.UserBookAsset
	require.NoError(t, db.First(&processedAsset, "id = ?", assetID).Error)
	require.Equal(t, model.BookAssetStatusPrivateAvailable, processedAsset.ProcessingStatus)
	require.Equal(t, "structurally_clean", processedAsset.ScanStatus)
	require.Len(t, processedAsset.SHA256, 64)
	var processedImport model.UserBookImport
	require.NoError(t, db.First(&processedImport, "id = ?", importID).Error)
	require.Equal(t, model.BookImportStatusMetadataReady, processedImport.Status)
	require.Contains(t, processedImport.MetadataJSON, `"structural_scan": "structurally_clean"`)
}

func TestProcessBookAssetsRejectsUnsafeEPUBAndKeepsItUnreadable(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.UserBookImport{}, &model.UserBookAsset{})
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	importID := uuid.New()
	assetID := uuid.New()
	key := "books/private/unsafe.epub"
	body := testEPUBBodyWithPath(t, "../escape.txt")
	require.NoError(t, db.Create(&model.UserBookImport{
		Base:               model.Base{ID: importID},
		UserID:             owner.ID,
		Title:              "不安全 EPUB",
		OriginalFilename:   "unsafe.epub",
		Format:             "epub",
		ContentType:        "application/epub+zip",
		SizeBytes:          int64(len(body)),
		ObjectKey:          key,
		Status:             model.BookImportStatusScanning,
		MetadataJSON:       "{}",
		CompletedPartsJSON: "[]",
	}).Error)
	require.NoError(t, db.Create(&model.UserBookAsset{
		Base:             model.Base{ID: assetID},
		ImportID:         importID,
		UserID:           owner.ID,
		OriginalFilename: "unsafe.epub",
		ContentType:      "application/epub+zip",
		Format:           "epub",
		SizeBytes:        int64(len(body)),
		ObjectKey:        key,
		ScanStatus:       "pending",
		ProcessingStatus: model.BookAssetStatusScanning,
	}).Error)

	processed, err := NewService(db).WithBookUploadStore(&fakeBookUploadStore{objects: map[string][]byte{key: body}}).ProcessBookAssets(context.Background())

	require.Equal(t, 1, processed)
	require.Error(t, err)
	var failedAsset model.UserBookAsset
	require.NoError(t, db.First(&failedAsset, "id = ?", assetID).Error)
	require.Equal(t, model.BookAssetStatusFailed, failedAsset.ProcessingStatus)
	var failedImport model.UserBookImport
	require.NoError(t, db.First(&failedImport, "id = ?", importID).Error)
	require.Equal(t, model.BookImportStatusFailed, failedImport.Status)
}

func TestPrivateBookReadingAndStateAreOwnerScoped(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.UserBookImport{}, &model.UserBookAsset{}, &model.UserBookReadingState{})
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	other := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	importID := uuid.New()
	assetID := uuid.New()
	key := "books/private/reader.txt"
	body := []byte("private book text")
	require.NoError(t, db.Create(&model.UserBookImport{
		Base:               model.Base{ID: importID},
		UserID:             owner.ID,
		Title:              "私有文本",
		OriginalFilename:   "reader.txt",
		Format:             "txt",
		ContentType:        "text/plain",
		SizeBytes:          int64(len(body)),
		ObjectKey:          key,
		Status:             model.BookImportStatusMetadataReady,
		MetadataJSON:       "{}",
		CompletedPartsJSON: "[]",
	}).Error)
	require.NoError(t, db.Create(&model.UserBookAsset{
		Base:             model.Base{ID: assetID},
		ImportID:         importID,
		UserID:           owner.ID,
		OriginalFilename: "reader.txt",
		ContentType:      "text/plain",
		Format:           "txt",
		SizeBytes:        int64(len(body)),
		SHA256:           "sha256",
		ObjectKey:        key,
		ScanStatus:       "structurally_clean",
		ProcessingStatus: model.BookAssetStatusPrivateAvailable,
	}).Error)
	service := NewService(db).WithBookUploadStore(&fakeBookUploadStore{objects: map[string][]byte{key: body}})

	state, err := service.GetBookReadingState(owner, assetID)
	require.NoError(t, err)
	require.Equal(t, assetID.String(), state.AssetID)
	require.Empty(t, state.EPUBCFI)
	require.Empty(t, state.Preferences)

	saved, err := service.SaveBookReadingState(owner, assetID, SaveBookReadingStateInput{
		TXTOffset:      7,
		ReadingPercent: 0.42,
		PrivateNotes:   "private note",
		Preferences:    map[string]any{"font_size": 18},
	})
	require.NoError(t, err)
	require.Equal(t, int64(7), saved.TXTOffset)
	require.Equal(t, 0.42, saved.ReadingPercent)
	require.Equal(t, float64(18), saved.Preferences["font_size"])

	content, err := service.OpenBookAsset(owner, assetID)
	require.NoError(t, err)
	contentBytes, readErr := io.ReadAll(content.Body)
	require.NoError(t, content.Body.Close())
	require.NoError(t, readErr)
	require.Equal(t, body, contentBytes)
	_, otherErr := service.GetBookReadingState(other, assetID)
	require.Error(t, otherErr)
}

func testEPUBBody(t *testing.T) []byte { return testEPUBBodyWithPath(t, "OEBPS/content.opf") }

func testEPUBBodyWithPath(t *testing.T, extraPath string) []byte {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	mimetype, err := archive.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	require.NoError(t, err)
	_, err = mimetype.Write([]byte("application/epub+zip"))
	require.NoError(t, err)
	container, err := archive.Create("META-INF/container.xml")
	require.NoError(t, err)
	_, err = container.Write([]byte(`<?xml version="1.0"?><container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`))
	require.NoError(t, err)
	content, err := archive.Create(extraPath)
	require.NoError(t, err)
	_, err = content.Write([]byte("content"))
	require.NoError(t, err)
	require.NoError(t, archive.Close())
	return buffer.Bytes()
}
