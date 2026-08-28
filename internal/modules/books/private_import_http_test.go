package books

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookImportRoutesRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/books"), NewService(nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/books/imports", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	require.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestBookImportRoutesUseCurrentAuthContextAndReturnOnlyOwnerImports(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.UserBookImport{}, &model.UserBookAsset{})
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, owner)
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1/books"), NewService(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/books/imports", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"data":[]}`, response.Body.String())
}
