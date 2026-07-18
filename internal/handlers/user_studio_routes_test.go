package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
)

func TestSetupUserRoutesDoesNotRegisterLegacyDefaultChannelEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	router := gin.New()
	SetupUserRoutes(router, db)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/users/me/default-channels", nil),
		httptest.NewRequest(http.MethodPatch, "/api/v1/users/me/default-channels/blog", nil),
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("expected %s %s to return 404, got %d: %s", request.Method, request.URL.Path, response.Code, response.Body.String())
		}
	}
}
