package middleware

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestIDMiddlewarePreservesIncomingID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, RequestID(c))
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "request-123")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Header().Get("X-Request-ID") != "request-123" || w.Body.String() != "request-123" {
		t.Fatalf("request id header=%q body=%q", w.Header().Get("X-Request-ID"), w.Body.String())
	}
}

func TestRequestIDMiddlewareGeneratesID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	w := httptest.NewRecorder()

	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if w.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected generated request id")
	}
}

func TestAccessLogMiddlewareIncludesRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	r := gin.New()
	r.Use(RequestIDMiddleware(), AccessLogMiddleware(log.New(&output, "", 0)))
	r.GET("/resource", func(c *gin.Context) { c.Status(http.StatusCreated) })
	req := httptest.NewRequest(http.MethodGet, "/resource", nil)
	req.Header.Set("X-Request-ID", "request-456")

	r.ServeHTTP(httptest.NewRecorder(), req)

	got := output.String()
	for _, want := range []string{`request_id="request-456"`, `method="GET"`, `path="/resource"`, `status=201`} {
		if !strings.Contains(got, want) {
			t.Fatalf("access log %q does not contain %q", got, want)
		}
	}
}
