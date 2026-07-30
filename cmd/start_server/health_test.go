package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
)

func TestHealthRoutesExposeLivenessAndReadiness(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerHealthRoutes(r, testdb.Open(t))

	for _, path := range []string{"/healthz", "/readyz"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
	}
}
