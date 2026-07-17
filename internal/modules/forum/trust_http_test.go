package forum

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestForumTrustMeAndAdminEndpoints(t *testing.T) {
	userRouter, db, user, _ := newForumHTTPTestRouter(t)

	response := performForumRequest(t, userRouter, http.MethodGet, "/api/v1/forum/trust/me", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("expected trust me 200, got %d: %s", response.Code, response.Body.String())
	}
	var me struct {
		Data struct {
			Level       int       `json:"level"`
			EvaluatedAt time.Time `json:"evaluated_at"`
			NextLevel   *int      `json:"next_level"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode trust me: %v", err)
	}
	if me.Data.Level != 0 || me.Data.EvaluatedAt.IsZero() || me.Data.NextLevel == nil || *me.Data.NextLevel != 1 {
		t.Fatalf("unexpected trust me response: %#v", me.Data)
	}

	response = performForumRequest(t, userRouter, http.MethodGet, "/api/v1/forum/trust/users/"+user.UUID.String(), nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected normal user forbidden, got %d: %s", response.Code, response.Body.String())
	}

	admin := model.User{Username: "trust-admin", Email: "trust-admin@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	adminRouter := gin.New()
	adminRouter.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: admin.UUID, Username: admin.Username, Role: admin.Role})
		c.Next()
	})
	RegisterRoutes(adminRouter.Group("/api/v1/forum"), NewService(db))

	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/forum/trust/users/" + user.UUID.String()},
		{http.MethodPost, "/api/v1/forum/trust/users/" + user.UUID.String() + "/evaluate"},
	} {
		response = performForumRequest(t, adminRouter, request.method, request.path, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("expected %s %s 200, got %d: %s", request.method, request.path, response.Code, response.Body.String())
		}
	}
}

func TestForumTrustEndpointAuthenticationAndMissingUserErrors(t *testing.T) {
	_, db, user, _ := newForumHTTPTestRouter(t)
	unauthenticated := gin.New()
	RegisterRoutes(unauthenticated.Group("/api/v1/forum"), NewService(db))
	response := performForumRequest(t, unauthenticated, http.MethodGet, "/api/v1/forum/trust/me", nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected trust me 401, got %d: %s", response.Code, response.Body.String())
	}
	response = performForumRequest(t, unauthenticated, http.MethodGet, "/api/v1/forum/trust/users/"+user.UUID.String(), nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected anonymous admin endpoint 401, got %d: %s", response.Code, response.Body.String())
	}

	admin := model.User{Username: "missing-admin", Email: "missing-admin@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	adminRouter := gin.New()
	adminRouter.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: admin.UUID, Role: admin.Role})
		c.Next()
	})
	RegisterRoutes(adminRouter.Group("/api/v1/forum"), NewService(db))
	missingID := uuid.New()
	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/forum/trust/users/" + missingID.String()},
		{http.MethodPost, "/api/v1/forum/trust/users/" + missingID.String() + "/evaluate"},
	} {
		response = performForumRequest(t, adminRouter, request.method, request.path, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("expected missing user 404, got %d: %s", response.Code, response.Body.String())
		}
	}
}
