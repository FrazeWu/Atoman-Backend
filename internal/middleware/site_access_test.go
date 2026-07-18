package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/service"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
)

func TestRequireSiteFeatureReturnsForbiddenWhenPublishingIsDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.SiteSetting{})

	input := service.DefaultSiteAccessMatrix().ToInput()
	blog := input.Modules["blog"]
	blog.Features["post.create"] = false
	input.Modules["blog"] = blog
	if err := service.NewSiteAccessService(db).SaveInput(input); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.POST("/posts", middleware.RequireSiteFeature(db, "blog", "post.create"), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/posts", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}
