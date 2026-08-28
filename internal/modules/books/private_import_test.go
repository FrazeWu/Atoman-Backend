package books

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestValidateBookUploadMetadataRequiresMatchingSupportedType(t *testing.T) {
	tests := []struct {
		name        string
		fileName    string
		contentType string
		size        int64
		format      string
		wantErr     bool
	}{
		{name: "epub", fileName: "novel.epub", contentType: "application/epub+zip", size: 1024, format: "epub"},
		{name: "pdf", fileName: "novel.pdf", contentType: "application/pdf", size: 1024, format: "pdf"},
		{name: "unsupported text", fileName: "novel.txt", contentType: "text/plain; charset=utf-8", size: 1024, wantErr: true},
		{name: "spoofed extension", fileName: "novel.pdf", contentType: "text/plain", size: 1024, wantErr: true},
		{name: "path traversal", fileName: "../novel.epub", contentType: "application/epub+zip", size: 1024, wantErr: true},
		{name: "empty file", fileName: "novel.txt", contentType: "text/plain", size: 0, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			format, err := validateBookUploadMetadata(test.fileName, test.contentType, test.size)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.format, format)
		})
	}
}

func TestValidateBookPublicationEvidenceAcceptsOnlyPDFAndEPUB(t *testing.T) {
	for _, test := range []struct {
		name        string
		fileName    string
		contentType string
		wantType    string
		wantErr     bool
	}{
		{name: "pdf", fileName: "permission.pdf", contentType: "application/pdf", wantType: "application/pdf"},
		{name: "epub", fileName: "permission.epub", contentType: "application/zip", wantType: "application/epub+zip"},
		{name: "text", fileName: "permission.txt", contentType: "text/plain", wantErr: true},
		{name: "png", fileName: "permission.png", contentType: "image/png", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, gotType, err := validateBookPublicationEvidence(test.fileName, test.contentType, 1024)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantType, gotType)
		})
	}
}
func TestBookPrivateObjectKeyDoesNotUseUserFilename(t *testing.T) {
	userID := uuid.New()
	importID := uuid.New()
	key := bookPrivateObjectKey(userID, importID, "epub")
	require.Equal(t, "books/private/users/"+userID.String()+"/imports/"+importID.String()+"/source.epub", key)
	require.NotContains(t, key, "original-name")
}

func TestInspectBookObjectRejectsMismatchedMagic(t *testing.T) {
	tests := []struct {
		name   string
		format string
		body   string
		valid  bool
	}{
		{name: "epub", format: "epub", body: "PK\x03\x04epub", valid: true},
		{name: "pdf", format: "pdf", body: "%PDF-1.7\npdf", valid: true},
		{name: "txt across chunk boundary", format: "txt", body: strings.Repeat("a", 511) + "中", valid: true},
		{name: "forged pdf", format: "pdf", body: "plain text", valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sha, err := inspectBookObject(bytes.NewBufferString(test.body), test.format, int64(len(test.body)))
			if test.valid {
				require.NoError(t, err)
				require.Len(t, sha, 64)
				return
			}
			require.Error(t, err)
		})
	}
}

func TestBookImportServiceScopesSessionsToOwnerAndRevokesOnDelete(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.UserBookImport{}, &model.UserBookAsset{})
	store := &fakeBookUploadStore{uploadID: "upload-1"}
	service := NewService(db).WithBookUploadStore(store)
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	other := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}

	session, err := service.CreateBookImport(owner, CreateBookImportInput{
		Title:       "A private novel",
		Author:      "An author",
		FileName:    "original-name.epub",
		ContentType: "application/epub+zip",
		Size:        1024,
	})
	require.NoError(t, err)
	require.Equal(t, model.BookImportStatusUploading, session.Status)
	require.Equal(t, "original-name.epub", session.FileName)
	require.NotContains(t, store.lastKey, "original-name")
	require.Equal(t, "upload-1", store.lastUploadID)

	ownerImports, err := service.ListBookImports(owner)
	require.NoError(t, err)
	require.Len(t, ownerImports, 1)
	otherImports, err := service.ListBookImports(other)
	require.NoError(t, err)
	require.Empty(t, otherImports)

	err = service.DeleteBookImport(other, uuid.MustParse(session.ID))
	require.Error(t, err)
	err = service.DeleteBookImport(owner, uuid.MustParse(session.ID))
	require.NoError(t, err)
	require.Equal(t, []string{store.lastKey}, store.deletedKeys)
	require.Equal(t, []string{store.lastKey}, store.abortedKeys)
	remaining, err := service.ListBookImports(owner)
	require.NoError(t, err)
	require.Empty(t, remaining)

	var deleted model.UserBookImport
	require.NoError(t, db.First(&deleted, "id = ?", session.ID).Error)
	require.Equal(t, model.BookImportStatusDeleted, deleted.Status)
}

func TestCompleteBookImportValidatesObjectAndCreatesScanningAsset(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.UserBookImport{}, &model.UserBookAsset{})
	body := []byte("%PDF-1.7\nprivate test book")
	store := &fakeBookUploadStore{uploadID: "upload-pdf", objects: map[string][]byte{}}
	service := NewService(db).WithBookUploadStore(store)
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}

	session, err := service.CreateBookImport(owner, CreateBookImportInput{
		Title:       "Private PDF",
		FileName:    "private.pdf",
		ContentType: "application/pdf",
		Size:        int64(len(body)),
	})
	require.NoError(t, err)
	store.objects[store.lastKey] = body
	importID := uuid.MustParse(session.ID)
	_, err = service.CompleteBookUploadPart(owner, importID, 1, BookUploadPart{ETag: "etag-1", Size: int64(len(body))})
	require.NoError(t, err)

	completed, err := service.CompleteBookImport(owner, importID)
	require.NoError(t, err)
	require.Equal(t, model.BookImportStatusScanning, completed.Status)
	require.Equal(t, model.BookAssetStatusScanning, completed.ProcessingStatus)
	require.Len(t, completed.AssetID, 36)
	require.Len(t, store.completedKeys, 1)

	var asset model.UserBookAsset
	require.NoError(t, db.Where("import_id = ?", importID).First(&asset).Error)
	require.Len(t, asset.SHA256, 64)
	require.Equal(t, model.BookAssetStatusScanning, asset.ProcessingStatus)
}

func TestDeleteBookImportRevokesStateAndCleansDerivedObjects(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.UserBookImport{}, &model.UserBookAsset{}, &model.UserBookReadingState{})
	store := &fakeBookUploadStore{objects: map[string][]byte{}}
	service := NewService(db).WithBookUploadStore(store)
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	importID := uuid.New()
	assetID := uuid.New()
	sourceKey := "books/private/source.epub"
	derivedKey := "books/private/derived.epub"
	completedAt := time.Now().UTC()
	require.NoError(t, db.Create(&model.UserBookImport{Base: model.Base{ID: importID}, UserID: owner.ID, Title: "Private", OriginalFilename: "private.epub", Format: "epub", ContentType: "application/epub+zip", SizeBytes: 4, ObjectKey: sourceKey, UploadID: "upload", CompletedAt: &completedAt, Status: model.BookImportStatusMetadataReady}).Error)
	require.NoError(t, db.Create(&model.UserBookAsset{Base: model.Base{ID: assetID}, ImportID: importID, UserID: owner.ID, OriginalFilename: "private.epub", Format: "epub", ContentType: "application/epub+zip", SizeBytes: 4, ObjectKey: sourceKey, DerivedObjectKey: derivedKey, ProcessingStatus: model.BookAssetStatusPrivateAvailable}).Error)
	require.NoError(t, db.Create(&model.UserBookReadingState{UserID: owner.ID, AssetID: assetID, PrivateNotes: "private"}).Error)

	require.NoError(t, service.DeleteBookImport(owner, importID))
	require.ElementsMatch(t, []string{sourceKey, derivedKey}, store.deletedKeys)
	require.Empty(t, store.abortedKeys)
	var deleted model.UserBookImport
	require.NoError(t, db.First(&deleted, "id = ?", importID).Error)
	require.Empty(t, deleted.ObjectKey)
	var asset model.UserBookAsset
	require.NoError(t, db.First(&asset, "id = ?", assetID).Error)
	require.Empty(t, asset.ObjectKey)
	require.Empty(t, asset.DerivedObjectKey)
	var state model.UserBookReadingState
	require.ErrorIs(t, db.First(&state, "asset_id = ?", assetID).Error, gorm.ErrRecordNotFound)
}

type fakeBookUploadStore struct {
	uploadID      string
	lastUploadID  string
	lastKey       string
	deletedKeys   []string
	abortedKeys   []string
	completedKeys []string
	objects       map[string][]byte
}

func (f *fakeBookUploadStore) CreateMultipartUpload(key, _ string) (string, error) {
	f.lastUploadID = f.uploadID
	f.lastKey = key
	return f.uploadID, nil
}

func (f *fakeBookUploadStore) PresignUploadPart(_ string, _ string, _ int, _ time.Duration) (string, error) {
	return "", errors.New("not implemented")
}

func (f *fakeBookUploadStore) CompleteMultipartUpload(key, _ string, _ []BookUploadPart) error {
	f.completedKeys = append(f.completedKeys, key)
	return nil
}

func (f *fakeBookUploadStore) ObjectSize(key string) (int64, error) {
	if f.objects == nil {
		return 0, errors.New("object not found")
	}
	body, ok := f.objects[key]
	if !ok {
		return 0, errors.New("object not found")
	}
	return int64(len(body)), nil
}

func (f *fakeBookUploadStore) OpenObject(key string) (io.ReadCloser, error) {
	if f.objects == nil {
		return nil, errors.New("object not found")
	}
	body, ok := f.objects[key]
	if !ok {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (f *fakeBookUploadStore) AbortMultipartUpload(key, _ string) error {
	f.abortedKeys = append(f.abortedKeys, key)
	return nil
}

func (f *fakeBookUploadStore) DeleteObject(key string) error {
	f.deletedKeys = append(f.deletedKeys, key)
	return nil
}

func (f *fakeBookUploadStore) PutObject(key, _ string, body io.ReadSeeker, size int64) error {
	data, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return errors.New("unexpected object size")
	}
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	f.objects[key] = data
	return nil
}

func (f *fakeBookUploadStore) CopyObject(sourceKey, destinationKey, _ string) error {
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	body, ok := f.objects[sourceKey]
	if !ok {
		return errors.New("source object not found")
	}
	f.objects[destinationKey] = append([]byte(nil), body...)
	return nil
}
