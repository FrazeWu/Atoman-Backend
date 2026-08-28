package books

import (
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

func TestPublicationRetentionHoldRouteUpdatesHoldForModerator(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookPublicationRequest{}, &model.AuditLog{})
	moderator := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleModerator}
	request := model.BookPublicationRequest{SubmittedBy: uuid.New(), AssetID: uuid.New(), Status: model.BookPublicationStatusPublished}
	require.NoError(t, db.Create(&request).Error)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, moderator)
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1/books"), NewService(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/books/review/publication-requests/"+request.ID.String()+"/retention-hold", strings.NewReader(`{"held":true,"reason":"legal hold"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"held":true`)
	var persisted model.BookPublicationRequest
	require.NoError(t, db.First(&persisted, "id = ?", request.ID).Error)
	require.True(t, persisted.RetentionHold)
}

func TestPublicationRetentionHoldRouteRejectsNonReviewer(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookPublicationRequest{}, &model.AuditLog{})
	request := model.BookPublicationRequest{SubmittedBy: uuid.New(), AssetID: uuid.New(), Status: model.BookPublicationStatusPublished}
	require.NoError(t, db.Create(&request).Error)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser})
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1/books"), NewService(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/books/review/publication-requests/"+request.ID.String()+"/retention-hold", strings.NewReader(`{"held":true,"reason":"legal hold"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	require.Equal(t, http.StatusForbidden, response.Code)
}
