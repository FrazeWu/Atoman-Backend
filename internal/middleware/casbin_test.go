package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/casbin/casbin/v3"
	casbinmodel "github.com/casbin/casbin/v3/model"
	fileadapter "github.com/casbin/casbin/v3/persist/file-adapter"
	"github.com/gin-gonic/gin"
)

func testCasbinModel(t *testing.T) casbinmodel.Model {
	t.Helper()
	m, err := casbinmodel.NewModelFromString(`
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && keyMatch2(r.obj, p.obj) && regexMatch(r.act, p.act)
`)
	if err != nil {
		t.Fatalf("create casbin model: %v", err)
	}
	return m
}

func TestCasbinMiddlewareAllowsAnonymousV1AuthLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var err error
	policyFile := t.TempDir() + "/policy.csv"
	if err := os.WriteFile(policyFile, nil, 0o600); err != nil {
		t.Fatalf("create policy file: %v", err)
	}
	Enforcer, err = casbin.NewEnforcer(testCasbinModel(t), fileadapter.NewAdapter(policyFile))
	if err != nil {
		t.Fatalf("create enforcer: %v", err)
	}
	initDefaultPolicies()

	r := gin.New()
	r.Use(CasbinMiddleware())
	r.POST("/api/v1/auth/login", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected login route to pass Casbin, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCasbinMiddlewareTreatsDeniedV1AuthPostsAsForbidden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var err error
	policyFile := t.TempDir() + "/policy.csv"
	if err := os.WriteFile(policyFile, []byte("p, anonymous, /api/v1/auth/login, POST\n"), 0o600); err != nil {
		t.Fatalf("create policy file: %v", err)
	}
	Enforcer, err = casbin.NewEnforcer(testCasbinModel(t), fileadapter.NewAdapter(policyFile))
	if err != nil {
		t.Fatalf("create enforcer: %v", err)
	}
	if err := Enforcer.LoadPolicy(); err != nil {
		t.Fatalf("load policy: %v", err)
	}

	r := gin.New()
	r.Use(CasbinMiddleware())
	r.POST("/api/v1/auth/register", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected denied v1 auth POST to be forbidden, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCasbinMiddlewareLetsUserWebSocketReachRouteAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var err error
	policyFile := t.TempDir() + "/policy.csv"
	if err := os.WriteFile(policyFile, nil, 0o600); err != nil {
		t.Fatalf("create policy file: %v", err)
	}
	Enforcer, err = casbin.NewEnforcer(testCasbinModel(t), fileadapter.NewAdapter(policyFile))
	if err != nil {
		t.Fatalf("create enforcer: %v", err)
	}
	initDefaultPolicies()

	r := gin.New()
	r.Use(CasbinMiddleware())
	r.GET("/ws/user", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws/user?token=browser-websocket-token", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected user websocket route to pass Casbin and handle its own token, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCasbinMiddlewareModeratorInheritsUserButNotAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	policyFile := t.TempDir() + "/policy.csv"
	if err := os.WriteFile(policyFile, nil, 0o600); err != nil {
		t.Fatalf("create policy file: %v", err)
	}
	var err error
	Enforcer, err = casbin.NewEnforcer(testCasbinModel(t), fileadapter.NewAdapter(policyFile))
	if err != nil {
		t.Fatalf("create enforcer: %v", err)
	}
	initDefaultPolicies()
	if _, err := Enforcer.AddPolicy("admin", "/admin-only", "POST"); err != nil {
		t.Fatalf("add admin-only policy: %v", err)
	}

	routerFor := func(role string) *gin.Engine {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("role", role)
			c.Next()
		})
		r.Use(CasbinMiddleware())
		r.POST("/api/v1/forum/topics", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		r.POST("/admin-only", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		return r
	}

	w := httptest.NewRecorder()
	routerFor("moderator").ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/forum/topics", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected moderator to inherit user API access, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	routerFor("moderator").ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin-only", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected moderator not to inherit admin access, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	routerFor("owner").ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin-only", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected owner to retain admin access, got %d: %s", w.Code, w.Body.String())
	}
}
