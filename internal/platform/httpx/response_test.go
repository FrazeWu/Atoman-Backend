package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/platform/apperr"

	"github.com/gin-gonic/gin"
)

func TestOKWritesDataEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		OK(c, http.StatusCreated, gin.H{"id": "one"})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	var body map[string]map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["data"]["id"] != "one" {
		t.Fatalf("expected data id one, got %#v", body)
	}
}

func TestListWritesPaginationEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		List(c, []string{"one"}, 2, 10, 25)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body struct {
		Data []string `json:"data"`
		Meta PageMeta `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Meta.Page != 2 || body.Meta.PageSize != 10 || body.Meta.Total != 25 || !body.Meta.HasMore {
		t.Fatalf("unexpected meta: %#v", body.Meta)
	}
}

func TestErrorWritesErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "blog.post_not_found" {
		t.Fatalf("expected blog.post_not_found, got %q", body.Error.Code)
	}
}

func TestPageParamsAndOffset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		page, pageSize := PageParams(c)
		OK(c, http.StatusOK, gin.H{"page": page, "page_size": pageSize, "offset": Offset(page, pageSize)})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/?page=3&page_size=40", nil))
	var body struct {
		Data struct {
			Page     int `json:"page"`
			PageSize int `json:"page_size"`
			Offset   int `json:"offset"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Page != 3 || body.Data.PageSize != 40 || body.Data.Offset != 80 {
		t.Fatalf("unexpected pagination data: %#v", body.Data)
	}
}

func TestPageParamsClampsPageSize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		page, pageSize := PageParams(c)
		OK(c, http.StatusOK, gin.H{"page": page, "page_size": pageSize})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/?page_size=101", nil))
	var body struct {
		Data struct {
			PageSize int `json:"page_size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.PageSize != 100 {
		t.Fatalf("expected page size 100, got %d", body.Data.PageSize)
	}
}

func TestPageParamsWithUsesNamedSizeAndConfiguredBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		page, limit := PageParamsWith(c, "limit", 50, 200)
		OK(c, http.StatusOK, gin.H{"page": page, "limit": limit})
	})
	for _, test := range []struct {
		name      string
		query     string
		wantLimit int
	}{
		{name: "configured value", query: "?page=2&limit=150", wantLimit: 150},
		{name: "over limit", query: "?limit=201", wantLimit: 50},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+test.query, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			var body struct {
				Data struct {
					Limit int `json:"limit"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Data.Limit != test.wantLimit {
				t.Fatalf("expected limit %d, got %d", test.wantLimit, body.Data.Limit)
			}
		})
	}
}
