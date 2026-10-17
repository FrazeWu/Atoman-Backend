package books

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The new module deliberately has no private-import, approval or legacy catalog API.
func TestBookLegacyEndpointsAreRetired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/books"), NewService(nil))
	for _, test := range []struct{ method, path string }{{"GET", "/catalog/search"}, {"GET", "/library"}, {"GET", "/imports"}, {"POST", "/imports"}, {"POST", "/contributions"}, {"GET", "/review/contributions"}, {"GET", "/publication-requests"}, {"GET", "/assets/old/content"}, {"GET", "/notes"}} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(test.method, "/api/v1/books"+test.path, nil))
		require.Equal(t, http.StatusNotFound, response.Code, test.path)
	}
}
