package books

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookRevisionContentChecksAllLifecycleLayers(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.BookWork{})
	require.NoError(t, migrations.RunBooksV2Migration(db))
	work := model.BookWork{Title: "公开图书", LifecycleStatus: model.BookLifecycleStatusActive}
	require.NoError(t, db.Create(&work).Error)
	asset := model.BookAsset{WorkID: work.ID, Format: "txt", Language: "zh", CreatedBy: uuid.New()}
	require.NoError(t, db.Create(&asset).Error)
	media := model.MediaAsset{Purpose: "books.file", Key: "private/revision.txt", ContentType: "text/plain", Size: 4}
	require.NoError(t, db.Create(&media).Error)
	revision := model.BookAssetRevision{AssetID: asset.ID, Number: 1, MediaAssetID: media.ID, SHA256: strings.Repeat("a", 64), Status: model.BookRevisionStatusReady, OriginalFilename: "book.txt", CreatedBy: asset.CreatedBy}
	require.NoError(t, db.Create(&revision).Error)
	store := &fakeBookUploadStore{objects: map[string][]byte{media.Key: []byte("book")}}
	service := NewService(db).WithBookResourceStore(store)
	content, err := service.OpenBookRevision(context.Background(), revision.ID)
	require.NoError(t, err)
	body, err := io.ReadAll(content.Body)
	require.NoError(t, err)
	require.NoError(t, content.Body.Close())
	require.Equal(t, "book", string(body))
	encoded, err := json.Marshal(revision)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "media_asset_id")
	require.NotContains(t, string(encoded), media.Key)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/books"), service)
	url := "/api/v1/books/asset-revisions/" + revision.ID.String() + "/content"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, url, nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))

	for _, status := range []string{model.BookRevisionStatusScanning, model.BookRevisionStatusQuarantined, model.BookRevisionStatusRemoved} {
		require.NoError(t, db.Model(&revision).Update("status", status).Error)
		_, err := service.OpenBookRevision(context.Background(), revision.ID)
		require.Error(t, err)
	}
	require.NoError(t, db.Model(&revision).Update("status", model.BookRevisionStatusReady).Error)
	require.NoError(t, db.Model(&asset).Update("status", model.BookResourceStatusRemoved).Error)
	_, err = service.OpenBookRevision(context.Background(), revision.ID)
	require.Error(t, err)
	require.NoError(t, db.Model(&asset).Update("status", model.BookResourceStatusActive).Error)
	require.NoError(t, db.Model(&work).Update("lifecycle_status", model.BookLifecycleStatusRetired).Error)
	_, err = service.OpenBookRevision(context.Background(), revision.ID)
	require.Error(t, err)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, url, nil))
	require.Equal(t, http.StatusNotFound, response.Code)
	require.NoError(t, db.Model(&work).Update("lifecycle_status", model.BookLifecycleStatusActive).Error)
	require.Error(t, db.Model(&media).Update("url", "https://cdn.example.test/unsafe.txt").Error)
	require.NoError(t, db.Delete(&media).Error)
	_, err = service.OpenBookRevision(context.Background(), revision.ID)
	require.Error(t, err)
}

func TestBookResourceStoreRequiresSeparatePrivateBucket(t *testing.T) {
	t.Setenv("S3_BUCKET", "public-media")
	t.Setenv("BOOK_PRIVATE_BUCKET", "")
	require.Nil(t, newPrivateBookResourceStore(&s3.S3{}))
	t.Setenv("BOOK_PRIVATE_BUCKET", "public-media")
	require.Nil(t, newPrivateBookResourceStore(&s3.S3{}))
	t.Setenv("BOOK_PRIVATE_BUCKET", "private-books")
	require.NotNil(t, newPrivateBookResourceStore(&s3.S3{}))
}
