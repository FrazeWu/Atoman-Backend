package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
)

func TestSiteVisitStatsRecordAndReadAggregates(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.SiteVisitDaily{})
	router := gin.New()
	router.GET("/visits", GetSiteVisitStats(db))
	router.POST("/visits", RecordSiteVisit(db))

	request := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
			req.Body = http.NoBody
		}
		router.ServeHTTP(w, req)
		return w
	}

	if response := request(http.MethodGet, "/visits"); response.Code != http.StatusOK || response.Body.String() == "" {
		t.Fatalf("expected initial stats response, got %d: %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPost, "/visits"); response.Code != http.StatusNoContent {
		t.Fatalf("expected 204 after recording visit, got %d: %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPost, "/visits"); response.Code != http.StatusNoContent {
		t.Fatalf("expected 204 after recording second visit, got %d: %s", response.Code, response.Body.String())
	}
	response := request(http.MethodGet, "/visits")
	if response.Code != http.StatusOK || response.Body.String() == "" {
		t.Fatalf("expected aggregate stats response, got %d: %s", response.Code, response.Body.String())
	}
}
