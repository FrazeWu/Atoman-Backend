package books

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestListPublishedBookAssetsHonorsPaginationQuery(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookSource{}, &model.BookRating{}, &model.UserBookAsset{}, &model.BookPublicationRequest{}, &model.PublishedBookAsset{})

	work := model.BookWork{Base: model.Base{ID: uuid.New()}, Title: "Paginated work", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&work).Error)

	for _, name := range []string{"first.pdf", "second.pdf"} {
		sourceID := uuid.New()
		require.NoError(t, db.Create(&model.UserBookAsset{
			Base:             model.Base{ID: sourceID},
			ImportID:         uuid.New(),
			UserID:           uuid.New(),
			OriginalFilename: name,
			Format:           "pdf",
			ContentType:      "application/pdf",
			SizeBytes:        1,
			ObjectKey:        "books/private/" + name,
			ProcessingStatus: model.BookAssetStatusPrivateAvailable,
		}).Error)
		require.NoError(t, db.Create(&model.PublishedBookAsset{
			Base:                 model.Base{ID: uuid.New()},
			PublicationRequestID: uuid.New(),
			SourceAssetID:        sourceID,
			WorkID:               &work.ID,
			Format:               "pdf",
			ObjectKey:            "books/public/" + name,
			Status:               model.BookPublicationStatusPublished,
		}).Error)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/books"), NewService(db))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/books/catalog/works/"+work.ID.String()+"/assets?limit=1&offset=1", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Data struct {
			Items  []BookPublishedAssetDTO `json:"items"`
			Total  int64                   `json:"total"`
			Limit  int                     `json:"limit"`
			Offset int                     `json:"offset"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, int64(2), payload.Data.Total)
	require.Equal(t, 1, payload.Data.Limit)
	require.Equal(t, 1, payload.Data.Offset)
	require.Len(t, payload.Data.Items, 1)
	require.Equal(t, "first.pdf", payload.Data.Items[0].FileName)
}
