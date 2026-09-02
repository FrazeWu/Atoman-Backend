package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/forum_moderation"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type middlewareSessionFixture struct {
	Token string
	CSRF  string
	Kind  string
}

func newMiddlewareAuthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Open(t)
	if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return db
}

func seedMiddlewareAuthUser(t *testing.T, db *gorm.DB, user model.User) model.User {
	t.Helper()
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create auth user: %v", err)
	}
	return user
}

func seedMiddlewareSession(t *testing.T, db *gorm.DB, user model.User, kind string) middlewareSessionFixture {
	t.Helper()
	credentials, err := authsession.New(db).Create(user.UUID, kind)
	if err != nil {
		t.Fatalf("create auth session: %v", err)
	}
	return middlewareSessionFixture{Token: credentials.Token, CSRF: credentials.CSRFToken, Kind: kind}
}

func performAuthRequest(t *testing.T, db *gorm.DB, method string, session middlewareSessionFixture, configure func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	SetAuthDB(db)
	t.Cleanup(func() { SetAuthDB(nil) })
	r := gin.New()
	r.Handle(method, "/protected", AuthMiddleware(), func(c *gin.Context) {
		current, ok := authctx.Current(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "missing auth context"})
			return
		}
		legacyUserID, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "missing legacy auth context"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"user_id":        current.ID.String(),
			"legacy_user_id": legacyUserID.(uuid.UUID).String(),
		})
	})
	req := httptest.NewRequest(method, "/protected", nil)
	if session.Token != "" {
		if session.Kind == authsession.KindAPI {
			req.Header.Set("Authorization", "Bearer "+session.Token)
		} else {
			req.AddCookie(&http.Cookie{Name: AuthSessionCookieName, Value: session.Token})
		}
	}
	if configure != nil {
		configure(req)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAuthMiddlewareRejectsMissingCredentials(t *testing.T) {
	w := performAuthRequest(t, newMiddlewareAuthTestDB(t), http.MethodGet, middlewareSessionFixture{}, nil)
	if w.Code != http.StatusUnauthorized || w.Body.String() != `{"error":{"code":"auth.unauthorized","details":{},"message":"Authorization required"}}` {
		t.Fatalf("unexpected response %d: %s", w.Code, w.Body.String())
	}
}

func TestTrustedOriginMiddlewareRejectsUntrustedProductionOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "production")
	t.Setenv("FRONTEND_URL", "https://www.atoman.org")
	r := gin.New()
	r.POST("/auth", TrustedOriginMiddleware(), func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodPost, "/auth", nil)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"auth.origin_invalid"`) {
		t.Fatalf("expected origin rejection, got %d: %s", w.Code, w.Body.String())
	}

	trusted := httptest.NewRequest(http.MethodPost, "/auth", nil)
	trusted.Header.Set("Origin", "https://www.atoman.org")
	trustedResponse := httptest.NewRecorder()
	r.ServeHTTP(trustedResponse, trusted)
	if trustedResponse.Code != http.StatusNoContent {
		t.Fatalf("expected trusted origin to pass, got %d", trustedResponse.Code)
	}
}

func TestIsTrustedWebOriginAcceptsConfiguredLocalDevelopmentPortsInProduction(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("FRONTEND_URL", "https://www.atoman.org")

	for _, origin := range []string{
		"http://localhost:5173",
		"http://localhost:5180",
		"http://127.0.0.1:5175",
		"http://localhost:52310",
		"http://127.0.0.1:52310",
	} {
		if !IsTrustedWebOrigin(origin) {
			t.Fatalf("expected local development origin %q to be trusted", origin)
		}
	}

	for _, origin := range []string{
		"http://localhost:5172",
		"http://localhost:5181",
		"http://example.com:5175",
	} {
		if IsTrustedWebOrigin(origin) {
			t.Fatalf("expected origin %q to be rejected", origin)
		}
	}
}

func TestStableAuthMiddlewareUsesStructuredErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	SetAuthDB(newMiddlewareAuthTestDB(t))
	t.Cleanup(func() { SetAuthDB(nil) })
	r := gin.New()
	r.GET("/protected", StableAuthMiddleware(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/protected", nil))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"auth.unauthorized"`) {
		t.Fatalf("expected stable auth body, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareAcceptsWebCookieForReadRequest(t *testing.T) {
	db := newMiddlewareAuthTestDB(t)
	user := seedMiddlewareAuthUser(t, db, model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true})
	session := seedMiddlewareSession(t, db, user, authsession.KindWeb)
	w := performAuthRequest(t, db, http.MethodGet, session, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), user.UUID.String()) {
		t.Fatalf("expected authenticated cookie request, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareAcceptsAPIBearerForWriteRequestWithoutCSRF(t *testing.T) {
	db := newMiddlewareAuthTestDB(t)
	user := seedMiddlewareAuthUser(t, db, model.User{Username: "client", Email: "client@example.com", Password: "hash", Role: "user", IsActive: true})
	session := seedMiddlewareSession(t, db, user, authsession.KindAPI)
	w := performAuthRequest(t, db, http.MethodPost, session, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected api bearer request to pass, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareAcceptsCaseInsensitiveBearerScheme(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "bearer token-value")
	c.Request = req

	credential := credentialFromRequest(c)
	if !credential.present {
		t.Fatal("expected authorization header to be detected")
	}
	if credential.kind != authsession.KindAPI {
		t.Fatalf("credential kind = %q, want %q", credential.kind, authsession.KindAPI)
	}
	if credential.token != "token-value" {
		t.Fatalf("credential token = %q, want token-value", credential.token)
	}
}

func TestAuthMiddlewareDoesNotFallBackToCookieWhenBearerIsInvalid(t *testing.T) {
	db := newMiddlewareAuthTestDB(t)
	user := seedMiddlewareAuthUser(t, db, model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true})
	session := seedMiddlewareSession(t, db, user, authsession.KindWeb)
	w := performAuthRequest(t, db, http.MethodGet, session, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer invalid-token")
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected invalid bearer to win, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareRejectsWebCookieWriteWithoutCSRF(t *testing.T) {
	t.Setenv("FRONTEND_URL", "https://www.atoman.org")
	db := newMiddlewareAuthTestDB(t)
	user := seedMiddlewareAuthUser(t, db, model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true})
	session := seedMiddlewareSession(t, db, user, authsession.KindWeb)
	w := performAuthRequest(t, db, http.MethodPost, session, func(req *http.Request) {
		req.Header.Set("Origin", "https://www.atoman.org")
	})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"auth.csrf_invalid"`) {
		t.Fatalf("expected csrf rejection, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareAcceptsWebCookieWriteWithTrustedOriginAndCSRF(t *testing.T) {
	t.Setenv("FRONTEND_URL", "https://www.atoman.org")
	db := newMiddlewareAuthTestDB(t)
	user := seedMiddlewareAuthUser(t, db, model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true})
	session := seedMiddlewareSession(t, db, user, authsession.KindWeb)
	w := performAuthRequest(t, db, http.MethodPost, session, func(req *http.Request) {
		req.Header.Set("Origin", "https://www.atoman.org")
		req.Header.Set(CSRFHeaderName, session.CSRF)
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected csrf-protected request to pass, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareRejectsRevokedAndExpiredSessions(t *testing.T) {
	db := newMiddlewareAuthTestDB(t)
	user := seedMiddlewareAuthUser(t, db, model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true})
	revoked := seedMiddlewareSession(t, db, user, authsession.KindAPI)
	if err := authsession.New(db).Revoke(revoked.Token); err != nil {
		t.Fatal(err)
	}
	if w := performAuthRequest(t, db, http.MethodGet, revoked, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected revoked token rejection, got %d", w.Code)
	}
	expired := seedMiddlewareSession(t, db, user, authsession.KindAPI)
	if err := db.Model(&model.AuthSession{}).
		Where("token_hash = ?", authsession.Hash(expired.Token)).
		Update("expires_at", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)).Error; err != nil {
		t.Fatal(err)
	}
	if w := performAuthRequest(t, db, http.MethodGet, expired, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected expired token rejection, got %d", w.Code)
	}
}

func TestForumBanImmediatelyInvalidatesExistingSession(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{}, &model.Notification{}, &model.AuditLog{}, &model.ForumUserModerationAction{})
	admin := model.User{Username: "ban-admin", Email: "ban-admin@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	target := model.User{Username: "ban-target", Email: "ban-target@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	session := seedMiddlewareSession(t, db, target, authsession.KindAPI)
	if w := performAuthRequest(t, db, http.MethodGet, session, nil); w.Code != http.StatusOK {
		t.Fatalf("expected session valid before ban, got %d", w.Code)
	}
	svc := forum_moderation.NewService(db)
	actor := authctx.CurrentUser{ID: admin.UUID, Username: admin.Username, Role: admin.Role}
	if _, err := svc.ApplyUserAction(actor, target.UUID, forum_moderation.UserActionRequest{Action: "ban", Reason: "违规"}); err != nil {
		t.Fatal(err)
	}
	if w := performAuthRequest(t, db, http.MethodGet, session, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after ban, got %d: %s", w.Code, w.Body.String())
	}
}
