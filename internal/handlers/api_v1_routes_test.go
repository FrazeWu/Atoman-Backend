package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
)

func TestProtectionRoutesMountUnderAPIV1Only(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.ContentProtection{})

	r := gin.New()
	SetupProtectionRoutes(r, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/albums/not-a-uuid/protection", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected v1 album protection route to be mounted, got 404")
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/albums/not-a-uuid/protection", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected legacy album protection route to be unmounted, got %d: %s", w.Code, w.Body.String())
	}
}
