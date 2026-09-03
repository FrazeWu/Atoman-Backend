package httpx

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestBindRequiredJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/", func(c *gin.Context) {
		var input struct {
			Name string `json:"name"`
		}
		if !BindRequiredJSON(c, &input) {
			return
		}
		OK(c, http.StatusOK, input)
	})

	valid := httptest.NewRecorder()
	router.ServeHTTP(valid, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"Atoman"}`)))
	if valid.Code != http.StatusOK {
		t.Fatalf("valid JSON status = %d, want 200", valid.Code)
	}

	invalid := httptest.NewRecorder()
	router.ServeHTTP(invalid, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":`)))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d, want 400", invalid.Code)
	}
	if got := invalid.Body.String(); got != `{"error":{"code":"validation.invalid_request","details":{},"message":"request body must be valid JSON"}}` {
		t.Fatalf("invalid JSON response = %s", got)
	}
}

func TestUUIDParam(t *testing.T) {
	gin.SetMode(gin.TestMode)
	want := uuid.New()
	router := gin.New()
	router.GET("/:id", func(c *gin.Context) {
		id, ok := UUIDParam(c, "id")
		if !ok {
			return
		}
		OK(c, http.StatusOK, id)
	})

	valid := httptest.NewRecorder()
	router.ServeHTTP(valid, httptest.NewRequest(http.MethodGet, "/"+want.String(), nil))
	if valid.Code != http.StatusOK {
		t.Fatalf("valid UUID status = %d, want 200", valid.Code)
	}

	invalid := httptest.NewRecorder()
	router.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/invalid", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid UUID status = %d, want 400", invalid.Code)
	}
	if got := invalid.Body.String(); got != `{"error":{"code":"validation.invalid_request","details":{},"message":"id must be a valid uuid"}}` {
		t.Fatalf("invalid UUID response = %s", got)
	}
}
