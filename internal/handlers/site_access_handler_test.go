package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
)

func newSiteAccessHandlerTestRouter(t *testing.T) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.SiteSetting{})

	router := gin.New()
	settings := router.Group("/api/v1/settings")
	settings.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{
			Username: "admin",
			Role:     authctx.RoleAdmin,
		})
		c.Next()
	})
	settings.GET("/site-access", GetSiteAccessHandler(db))
	settings.PUT("/site-access", UpdateSiteAccessHandler(db))

	public := router.Group("/api/v1/settings/public")
	public.GET("/site-access", GetPublicSiteAccessHandler(db))

	return router
}

func TestGetSiteAccessHandlerReturnsStructuredSettings(t *testing.T) {
	router := newSiteAccessHandlerTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/site-access", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Settings struct {
			Feed struct {
				AllowManageSources bool   `json:"allow_manage_sources"`
				AllowAddSource     bool   `json:"allow_add_source"`
				FullTextMode       string `json:"full_text_mode"`
			} `json:"feed"`
			Forum struct {
				AllowCategoryRequest bool `json:"allow_category_request"`
				ModeratorPermissions struct {
					ReviewCategoryRequest bool `json:"review_category_request"`
					PinTopic              bool `json:"pin_topic"`
					LockTopic             bool `json:"lock_topic"`
				} `json:"moderator_permissions"`
			} `json:"forum"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if !payload.Settings.Feed.AllowManageSources {
		t.Fatal("expected allow_manage_sources to default true")
	}
	if !payload.Settings.Feed.AllowAddSource {
		t.Fatal("expected allow_add_source to default true")
	}
	if payload.Settings.Feed.FullTextMode != "per_source" {
		t.Fatalf("expected full_text_mode per_source, got %q", payload.Settings.Feed.FullTextMode)
	}
	if !payload.Settings.Forum.AllowCategoryRequest {
		t.Fatal("expected allow_category_request to default true")
	}
	if !payload.Settings.Forum.ModeratorPermissions.PinTopic {
		t.Fatal("expected moderator pin_topic permission to default true")
	}
	if !payload.Settings.Forum.ModeratorPermissions.LockTopic {
		t.Fatal("expected moderator lock_topic permission to default true")
	}
}

func TestUpdateSiteAccessHandlerPersistsStructuredSettings(t *testing.T) {
	router := newSiteAccessHandlerTestRouter(t)

	body := bytes.NewBufferString(`{
	  "version": 1,
	  "settings": {
	    "blog": {
	      "comment_mode": "all"
	    },
	    "forum": {
	      "allow_category_request": false
	    }
	  }
	}`)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/site-access", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var updated struct {
		Revision int `json:"revision"`
		Settings struct {
			Forum struct {
				AllowCategoryRequest bool `json:"allow_category_request"`
			} `json:"forum"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated.Settings.Forum.AllowCategoryRequest {
		t.Fatal("expected allow_category_request false after update")
	}
	if updated.Revision == 0 {
		t.Fatal("expected non-zero revision after save")
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/settings/site-access", nil)
	getRR := httptest.NewRecorder()
	router.ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("expected get-after-save 200, got %d: %s", getRR.Code, getRR.Body.String())
	}

	var reloaded struct {
		Settings struct {
			Forum struct {
				AllowCategoryRequest bool `json:"allow_category_request"`
			} `json:"forum"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(getRR.Body.Bytes(), &reloaded); err != nil {
		t.Fatalf("decode get-after-save response: %v", err)
	}
	if reloaded.Settings.Forum.AllowCategoryRequest {
		t.Fatal("expected persisted allow_category_request false")
	}
}
