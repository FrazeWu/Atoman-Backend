package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/ratelimit"

	"github.com/gin-gonic/gin"
	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openClientErrorDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/client-errors.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite pool: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.ClientErrorDaily{}); err != nil {
		t.Fatalf("migrate client errors: %v", err)
	}
	return db
}

func clientErrorRouter(t *testing.T, db *gorm.DB, limit int, now time.Time) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/client-errors", recordClientError(db, ratelimit.New(), limit, time.Minute, func() time.Time { return now }))
	router.GET("/api/v1/admin/client-errors/summary", GetClientErrorSummary(db))
	return router
}

func postClientError(router *gin.Engine, body string, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/client-errors", strings.NewReader(body))
	request.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestRecordClientErrorAggregatesByReleaseAndRoute(t *testing.T) {
	db := openClientErrorDB(t)
	now := time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC)
	router := clientErrorRouter(t, db, 30, now)
	body := `{
		"release":"v0.3.0",
		"route":"/blog/:id?token=secret&email=ada@example.com",
		"kind":"vue",
		"name":"TypeError",
		"message":"render failed for ada@example.com",
		"stack":"at render (https://www.atoman.org/assets/app.js?token=secret)\nBearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0.signature",
		"context":"Vue: render",
		"user_agent":"Mozilla/5.0 ada@example.com"
	}`
	for i := 0; i < 2; i++ {
		response := postClientError(router, body, "https://www.atoman.org")
		if response.Code != http.StatusNoContent {
			t.Fatalf("report %d: expected 204, got %d %s", i, response.Code, response.Body.String())
		}
	}
	other := `{"release":"v0.3.0","route":"/feed","kind":"chunk","name":"Error","message":"loading chunk 8 failed","stack":"","context":"chunk","user_agent":"Test"}`
	if response := postClientError(router, other, ""); response.Code != http.StatusNoContent {
		t.Fatalf("expected second route to record, got %d %s", response.Code, response.Body.String())
	}

	var row model.ClientErrorDaily
	if err := db.Where("route = ?", "/blog/:id").First(&row).Error; err != nil {
		t.Fatalf("load blog aggregate: %v", err)
	}
	if row.Count != 2 || row.Release != "v0.3.0" || row.Kind != "vue" || row.Day != "2026-11-16" {
		t.Fatalf("unexpected aggregate: %+v", row)
	}
	if strings.Contains(row.SampleMessage, "ada@example.com") || strings.Contains(row.SampleStack, "token=") || strings.Contains(row.SampleStack, "eyJ") || strings.Contains(row.SampleUserAgent, "@") {
		t.Fatalf("stored sample kept a secret: %+v", row)
	}
	if !strings.Contains(row.SampleMessage, "[redacted]") || strings.Contains(row.Route, "?") {
		t.Fatalf("expected redacted message and query-free route, got %+v", row)
	}

	summary := httptest.NewRecorder()
	router.ServeHTTP(summary, httptest.NewRequest(http.MethodGet, "/api/v1/admin/client-errors/summary?from=2026-11-16&to=2026-11-16", nil))
	if summary.Code != http.StatusOK {
		t.Fatalf("summary status %d: %s", summary.Code, summary.Body.String())
	}
	var payload struct {
		Data ClientErrorSummary `json:"data"`
	}
	if err := json.Unmarshal(summary.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if len(payload.Data.Groups) != 2 {
		t.Fatalf("expected two release/route groups, got %+v", payload.Data.Groups)
	}
	found := false
	for _, group := range payload.Data.Groups {
		if group.Release == "v0.3.0" && group.Route == "/blog/:id" {
			found = group.Count == 2 && group.Kinds["vue"] == 2
		}
	}
	if !found {
		t.Fatalf("missing blog error rate group: %+v", payload.Data.Groups)
	}
}

func TestRecordClientErrorRejectsSecretsOversizedBodiesAndUntrustedOrigins(t *testing.T) {
	db := openClientErrorDB(t)
	router := clientErrorRouter(t, db, 30, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))

	cases := []struct {
		name   string
		body   string
		origin string
		status int
	}{
		{name: "missing release", body: `{"route":"/feed","kind":"window"}`, status: http.StatusBadRequest},
		{name: "email route", body: `{"release":"v1","route":"/users/ada@example.com","kind":"window"}`, status: http.StatusBadRequest},
		{name: "bad kind", body: `{"release":"v1","route":"/feed","kind":"api"}`, status: http.StatusBadRequest},
		{name: "untrusted origin", body: `{"release":"v1","route":"/feed","kind":"window","message":"x"}`, origin: "https://evil.example", status: http.StatusForbidden},
	}
	for _, tc := range cases {
		response := postClientError(router, tc.body, tc.origin)
		if response.Code != tc.status {
			t.Fatalf("%s: expected %d, got %d %s", tc.name, tc.status, response.Code, response.Body.String())
		}
	}

	oversized := httptest.NewRequest(http.MethodPost, "/api/v1/client-errors", bytes.NewReader(bytes.Repeat([]byte("a"), maxClientErrorBody+1)))
	oversized.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, oversized)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d %s", response.Code, response.Body.String())
	}

	var count int64
	if err := db.Model(&model.ClientErrorDaily{}).Count(&count).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("rejected reports were stored: %d", count)
	}
}

func TestRecordClientErrorRateLimitsPerIP(t *testing.T) {
	db := openClientErrorDB(t)
	router := clientErrorRouter(t, db, 2, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	body := `{"release":"v0.3.0","route":"/feed","kind":"window","message":"boom"}`
	for i := 0; i < 2; i++ {
		if response := postClientError(router, body, ""); response.Code != http.StatusNoContent {
			t.Fatalf("request %d: expected 204, got %d", i, response.Code)
		}
	}
	limited := postClientError(router, body, "")
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("expected rate limit with Retry-After, got %d %q %s", limited.Code, limited.Header().Get("Retry-After"), limited.Body.String())
	}
}

func TestClientErrorSummaryRequiresAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openClientErrorDB(t)
	router := gin.New()
	SetupClientErrorRoutes(router, db)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/client-errors/summary", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected anonymous summary to be unauthorized, got %d %s", response.Code, response.Body.String())
	}
}

func TestClientErrorIngestIsCSRFExempt(t *testing.T) {
	if !middleware.ExemptFromWebCSRF(http.MethodPost, "/api/v1/client-errors") {
		t.Fatal("expected client error ingest to skip CSRF so sendBeacon can deliver it")
	}
	if middleware.ExemptFromWebCSRF(http.MethodPost, "/api/v1/auth/login") {
		t.Fatal("login must keep CSRF checks")
	}
}
