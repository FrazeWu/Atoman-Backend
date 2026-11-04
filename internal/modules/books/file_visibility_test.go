package books

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPrivateBookFileExplicitPublishingAndRevocation(t *testing.T) {
	s, owner, work, store := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	edition, err := s.CreateBookEdition(ctx, owner, workID, CreateBookEditionInput{Language: "zh", Publisher: "测试出版社", EditionStatement: "初版"})
	require.NoError(t, err)
	editionID := uuid.MustParse(edition.ID)
	asset, err := s.CreateBookResource(ctx, owner, workID, CreateBookResourceInput{EditionID: &editionID, Format: "txt", Language: "zh"})
	require.NoError(t, err)
	body := []byte("private file")
	upload, err := s.CreateBookResourceUpload(ctx, owner, asset.ID, CreateBookResourceUploadInput{
		FileName: "private.txt", ContentType: "text/plain", Size: int64(len(body)), PublicDistributionConfirmed: false,
	})
	require.NoError(t, err)
	store.objects[store.lastKey] = body
	_, err = s.CompleteBookResourceUploadPart(ctx, owner, upload.ID, 1, BookUploadPart{ETag: "etag", Size: int64(len(body))})
	require.NoError(t, err)
	_, err = s.CompleteBookResourceUpload(ctx, owner, upload.ID)
	require.NoError(t, err)
	_, err = s.ProcessBookResourceUploads(ctx)
	require.NoError(t, err)
	ready, err := s.GetBookResourceUpload(ctx, owner, upload.ID)
	require.NoError(t, err)
	require.NotNil(t, ready.RevisionID)
	id := *ready.RevisionID

	gin.SetMode(gin.TestMode)
	fileURL := "/api/v1/books/asset-revisions/" + id.String() + "/content"
	request := func(as *authctx.CurrentUser) int {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			if as != nil {
				authctx.SetCurrentUser(c, *as)
			}
			c.Next()
		})
		RegisterRoutes(router.Group("/api/v1/books"), s)
		writer := httptest.NewRecorder()
		router.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, fileURL, nil))
		return writer.Code
	}
	require.Equal(t, http.StatusNotFound, request(nil))
	require.Equal(t, http.StatusOK, request(&owner))
	other := authctx.CurrentUser{ID: uuid.New()}
	require.NoError(t, s.db.Create(&model.User{UUID: other.ID, Username: "other-reader", Email: "other-reader@example.test", Password: "hash", IsActive: true}).Error)
	require.Equal(t, http.StatusNotFound, request(&other))
	publicList, err := s.ListBookEditionFilesForViewer(ctx, editionID, uuid.Nil)
	require.NoError(t, err)
	require.Empty(t, publicList)
	_, err = s.SetBookFileVisibility(ctx, owner, id, true, false)
	require.Error(t, err)
	visibility, err := s.SetBookFileVisibility(ctx, owner, id, true, true)
	require.NoError(t, err)
	require.True(t, visibility.Public)
	require.Equal(t, http.StatusOK, request(nil))
	publicList, err = s.ListBookEditionFilesForViewer(ctx, editionID, uuid.Nil)
	require.NoError(t, err)
	require.Len(t, publicList, 1)
	_, err = s.SaveBookFileShelf(ctx, other, id, SaveBookFileShelfInput{})
	require.NoError(t, err)
	_, err = s.SetBookFileVisibility(ctx, owner, id, false, false)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, request(nil))
	row, total, err := s.ListBookFileShelf(ctx, other, "", "added", 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, row, 1)
	require.False(t, row[0].FileAvailable)
}
