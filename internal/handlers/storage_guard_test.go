package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireS3ReturnsStorageUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/upload", func(c *gin.Context) {
		if !requireS3(c, nil) {
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/upload", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
	want := `{"code":"storage.unavailable","error":"Storage service is unavailable"}`
	if w.Body.String() != want {
		t.Fatalf("body = %q, want %q", w.Body.String(), want)
	}
}
