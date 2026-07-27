package lifecycle

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
)

func lifecycleRouter(service *Service, user authctx.CurrentUser) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	group := r.Group("/api/v1/content")
	group.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, user); c.Next() })
	RegisterRoutes(group, service)
	return r
}

func TestLifecycleHTTPRecordsProgressAndListsContinue(t *testing.T) {
	fixture := newLifecycleFixture(t)
	r := lifecycleRouter(fixture.service, fixture.viewer)
	body := `{"module":"blog","content_id":"` + fixture.post.ID.String() + `","progress":0.5,"position_sec":60,"duration_sec":120,"source":"discover"}`
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/v1/content/progress", bytes.NewBufferString(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("save progress: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/content/continue?module=blog", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list continue: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Data []ContinueItem `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 || response.Data[0].ContentID != fixture.post.ID {
		t.Fatalf("unexpected response: %s", w.Body.String())
	}
}

func TestLifecycleHTTPSchedulesOwnedContent(t *testing.T) {
	fixture := newLifecycleFixture(t)
	if err := fixture.db.Model(&fixture.post).Updates(map[string]any{"status": "draft", "published_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	r := lifecycleRouter(fixture.service, fixture.owner)
	publishAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	body := `{"publish_at":"` + publishAt + `"}`
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/content/blog/"+fixture.post.ID.String()+"/schedule", bytes.NewBufferString(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("schedule: %d %s", w.Code, w.Body.String())
	}
}
