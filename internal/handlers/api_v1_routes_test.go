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

func TestMusicEntityUpdatesOnlyExposeRevisionRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	r := gin.New()
	SetupAlbumRoutes(r, db, nil)
	SetupArtistWikiRoutes(r, db, nil)
	SetupRevisionRoutes(r, db, nil)

	routes := make(map[string]bool)
	for _, route := range r.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"POST /api/v1/albums/:id/revisions",
		"POST /api/v1/artists/:id/revisions",
	} {
		if !routes[route] {
			t.Fatalf("expected revision route %q to be mounted", route)
		}
	}
	for _, route := range []string{
		"PUT /api/v1/albums/:id",
		"PUT /api/v1/artists/:id",
		"POST /api/v1/artists/:id/edit",
	} {
		if routes[route] {
			t.Fatalf("expected legacy update route %q to be unmounted", route)
		}
	}
	for _, route := range []string{
		"POST /api/v1/admin/reviews/revisions/:id/approve",
		"POST /api/v1/admin/reviews/revisions/:id/reject",
	} {
		if routes[route] {
			t.Fatalf("expected revision review route %q to be removed", route)
		}
	}
}
