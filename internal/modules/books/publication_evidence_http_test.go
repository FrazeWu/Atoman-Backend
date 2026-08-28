package books

import (
	"bytes"
	"mime/multipart"
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
)

func TestPublicationAppealRoutesSubmitAndRestoreRemovedAsset(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookPublicationRequest{}, &model.PublishedBookAsset{}, &model.BookPublicationAppeal{}, &model.AuditLog{})
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	moderator := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleModerator}
	publication := model.BookPublicationRequest{SubmittedBy: owner.ID, AssetID: uuid.New(), Status: model.BookPublicationStatusPublished}
	require.NoError(t, db.Create(&publication).Error)
	asset := model.PublishedBookAsset{PublicationRequestID: publication.ID, SourceAssetID: uuid.New(), Format: "pdf", ObjectKey: "books/public/assets/asset.pdf", Status: model.BookPublicationStatusRemoved}
	require.NoError(t, db.Create(&asset).Error)

	viewer := owner
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, viewer)
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1/books"), NewService(db))

	body := strings.NewReader(`{"reason":"请复核授权链路并恢复正文"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/books/publication-requests/"+publication.ID.String()+"/appeals", body)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	require.Equal(t, http.StatusCreated, response.Code)

	var appeal model.BookPublicationAppeal
	require.NoError(t, db.First(&appeal, "publication_request_id = ?", publication.ID).Error)
	viewer = moderator
	req = httptest.NewRequest(http.MethodPost, "/api/v1/books/review/publication-appeals/"+appeal.ID.String()+"/decision", strings.NewReader(`{"decision":"approved","note":"复核通过"}`))
	req.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	require.Equal(t, http.StatusOK, response.Code)

	var restored model.PublishedBookAsset
	require.NoError(t, db.First(&restored, "id = ?", asset.ID).Error)
	require.Equal(t, model.BookPublicationStatusPublished, restored.Status)
}

func TestUploadPublicationEvidenceRouteStoresMultipartFileInPrivateR2(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookPublicationRequest{}, &model.BookRightsDeclaration{})
	store := &fakeBookUploadStore{objects: map[string][]byte{}}
	service := NewService(db).WithBookUploadStore(store)
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	request := model.BookPublicationRequest{SubmittedBy: owner.ID, AssetID: uuid.New(), Status: model.BookPublicationStatusPendingReview}
	require.NoError(t, db.Create(&request).Error)
	require.NoError(t, db.Create(&model.BookRightsDeclaration{RequestID: request.ID, LicenseType: "authorized_distribution", Declaration: "I have permission"}).Error)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("evidence", "permission.pdf")
	require.NoError(t, err)
	_, err = part.Write([]byte("%PDF-1.7 evidence"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, owner)
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1/books"), service)
	requestURL := "/api/v1/books/publication-requests/" + request.ID.String() + "/evidence"
	req := httptest.NewRequest(http.MethodPost, requestURL, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"evidence_uploaded":true`)
	var rights model.BookRightsDeclaration
	require.NoError(t, db.First(&rights, "request_id = ?", request.ID).Error)
	require.NotEmpty(t, rights.EvidenceObjectKey)
	require.Equal(t, []byte("%PDF-1.7 evidence"), store.objects[rights.EvidenceObjectKey])
}
