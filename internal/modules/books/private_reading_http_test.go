package books

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBookReadingRoutesStreamPrivateContentAndPersistOwnerState(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.UserBookImport{}, &model.UserBookAsset{}, &model.UserBookReadingState{})
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	importID, assetID := uuid.New(), uuid.New()
	key := "books/private/http-reader.txt"
	body := []byte("private HTTP reader body")
	seedReadyBookAsset(t, db, owner.ID, importID, assetID, key, body)
	service := NewService(db).WithBookUploadStore(&fakeBookUploadStore{objects: map[string][]byte{key: body}})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, owner)
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1/books"), service)

	contentRequest := httptest.NewRequest(http.MethodGet, "/api/v1/books/assets/"+assetID.String()+"/content", nil)
	contentResponse := httptest.NewRecorder()
	router.ServeHTTP(contentResponse, contentRequest)
	require.Equal(t, http.StatusOK, contentResponse.Code)
	require.Equal(t, string(body), contentResponse.Body.String())
	require.Equal(t, "private, no-store", contentResponse.Header().Get("Cache-Control"))
	require.Contains(t, contentResponse.Header().Get("Content-Disposition"), "reader.txt")

	stateRequest := httptest.NewRequest(http.MethodPut, "/api/v1/books/assets/"+assetID.String()+"/reading-state", strings.NewReader(`{"txt_offset":9,"reading_percent":0.25,"private_notes":"only me","preferences":{"font_size":18}}`))
	stateRequest.Header.Set("Content-Type", "application/json")
	stateResponse := httptest.NewRecorder()
	router.ServeHTTP(stateResponse, stateRequest)
	require.Equal(t, http.StatusOK, stateResponse.Code)
	var stateEnvelope struct {
		Data BookReadingStateDTO `json:"data"`
	}
	require.NoError(t, json.Unmarshal(stateResponse.Body.Bytes(), &stateEnvelope))
	require.Equal(t, int64(9), stateEnvelope.Data.TXTOffset)
	require.Equal(t, 0.25, stateEnvelope.Data.ReadingPercent)
	require.Equal(t, "only me", stateEnvelope.Data.PrivateNotes)

	otherRouter := gin.New()
	other := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	otherRouter.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, other)
		c.Next()
	})
	RegisterRoutes(otherRouter.Group("/api/v1/books"), service)
	otherRequest := httptest.NewRequest(http.MethodGet, "/api/v1/books/assets/"+assetID.String()+"/content", nil)
	otherResponse := httptest.NewRecorder()
	otherRouter.ServeHTTP(otherResponse, otherRequest)
	require.Equal(t, http.StatusNotFound, otherResponse.Code)
}

func seedReadyBookAsset(t *testing.T, db *gorm.DB, userID, importID, assetID uuid.UUID, key string, body []byte) {
	t.Helper()
	require.NoError(t, db.Create(&model.UserBookImport{
		Base:               model.Base{ID: importID},
		UserID:             userID,
		Title:              "HTTP reader",
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
		UserID:           userID,
		OriginalFilename: "reader.txt",
		ContentType:      "text/plain",
		Format:           "txt",
		SizeBytes:        int64(len(body)),
		SHA256:           "sha256",
		ObjectKey:        key,
		ScanStatus:       "structurally_clean",
		ProcessingStatus: model.BookAssetStatusPrivateAvailable,
	}).Error)
}
