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
	"atoman/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newMiddlewareAuthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Open(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return db
}

func signedMiddlewareAuthTokenForTest(t *testing.T, user model.User) string {
	t.Helper()
	claims := jwt.MapClaims{
		"user_id":  user.UUID.String(),
		"username": user.Username,
		"role":     user.Role,
		"exp":      time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func seedMiddlewareAuthUser(t *testing.T, db *gorm.DB, user model.User) model.User {
	t.Helper()
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create auth user: %v", err)
	}
	return user
}

func performAuthRequest(authHeader string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/protected", AuthMiddleware(), func(c *gin.Context) {
		current, ok := authctx.Current(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "missing auth context"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "user_id": current.ID.String(), "username": current.Username, "role": current.Role})
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAuthMiddlewareRejectsMissingAuthorizationHeader(t *testing.T) {
	w := performAuthRequest("")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", w.Code)
	}
	if w.Body.String() != `{"error":"Authorization header required"}` {
		t.Fatalf("expected legacy auth body, got %s", w.Body.String())
	}
}

func TestStableAuthMiddlewareUsesStructuredErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/protected", StableAuthMiddleware(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/protected", nil))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"auth.unauthorized"`) {
		t.Fatalf("expected stable auth body, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareRejectsJWTWhenAuthDBMissing(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	SetAuthDB(nil)
	user := model.User{UUID: uuid.New(), Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}

	w := performAuthRequest("Bearer " + signedMiddlewareAuthTokenForTest(t, user))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Invalid token") {
		t.Fatalf("expected invalid token response, got %s", w.Body.String())
	}
}

func TestAuthMiddlewareRejectsInactiveJWTUser(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	db := newMiddlewareAuthTestDB(t)
	inactive := model.User{Username: "inactive", Email: "inactive@example.com", Password: "hash", Role: "user", IsActive: false}
	if err := db.Create(&inactive).Error; err != nil {
		t.Fatalf("create inactive user: %v", err)
	}
	if err := db.Model(&inactive).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate user: %v", err)
	}
	SetAuthDB(db)
	t.Cleanup(func() { SetAuthDB(nil) })

	w := performAuthRequest("Bearer " + signedMiddlewareAuthTokenForTest(t, inactive))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Invalid token") {
		t.Fatalf("expected invalid token response, got %s", w.Body.String())
	}
}

func TestForumBanImmediatelyInvalidatesExistingJWT(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Notification{}, &model.AuditLog{}, &model.ForumUserModerationAction{})
	admin := model.User{Username: "ban-admin", Email: "ban-admin@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	target := model.User{Username: "ban-target", Email: "ban-target@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	token := signedMiddlewareAuthTokenForTest(t, target)
	SetAuthDB(db)
	t.Cleanup(func() { SetAuthDB(nil) })
	if w := performAuthRequest("Bearer " + token); w.Code != http.StatusOK {
		t.Fatalf("expected token valid before ban, got %d", w.Code)
	}
	svc := forum_moderation.NewService(db)
	actor := authctx.CurrentUser{ID: admin.UUID, Username: admin.Username, Role: admin.Role}
	if _, err := svc.ApplyUserAction(actor, target.UUID, forum_moderation.UserActionRequest{Action: "ban", Reason: "违规"}); err != nil {
		t.Fatal(err)
	}
	if w := performAuthRequest("Bearer " + token); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after ban, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareRejectsInvalidToken(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	w := performAuthRequest("Bearer invalid.token")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", w.Code)
	}
}

func TestAuthMiddlewareAcceptsSharedAuthCookieWhenAuthorizationHeaderMissing(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	db := newMiddlewareAuthTestDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/protected", AuthMiddleware(), func(c *gin.Context) {
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

	user := seedMiddlewareAuthUser(t, db, model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true})
	SetAuthDB(db)
	t.Cleanup(func() { SetAuthDB(nil) })
	claims := jwt.MapClaims{
		"user_id":  user.UUID.String(),
		"username": "alice",
		"role":     "user",
		"exp":      time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: "atoman_token", Value: signed})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, user.UUID.String()) {
		t.Fatalf("expected response body to include user ID, got %s", body)
	}
	if !strings.Contains(body, `"legacy_user_id":"`+user.UUID.String()+`"`) {
		t.Fatalf("expected response body to include legacy user ID, got %s", body)
	}
}

func TestAuthMiddlewareFallsBackToSharedAuthCookieWhenAuthorizationHeaderIsInvalid(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	db := newMiddlewareAuthTestDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/protected", AuthMiddleware(), func(c *gin.Context) {
		current, ok := authctx.Current(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "missing auth context"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"user_id":  current.ID.String(),
			"username": current.Username,
			"role":     current.Role,
		})
	})

	user := seedMiddlewareAuthUser(t, db, model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true})
	SetAuthDB(db)
	t.Cleanup(func() { SetAuthDB(nil) })
	claims := jwt.MapClaims{
		"user_id":  user.UUID.String(),
		"username": "alice",
		"role":     "user",
		"exp":      time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer invalid.token")
	req.AddCookie(&http.Cookie{Name: "atoman_token", Value: signed})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), user.UUID.String()) {
		t.Fatalf("expected response body to include cookie user ID, got %s", w.Body.String())
	}
}

func TestOptionalAuthMiddlewareDoesNotSetCurrentUserWhenAuthDBMissing(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	SetAuthDB(nil)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/optional", OptionalAuthMiddleware(), func(c *gin.Context) {
		_, ok := authctx.Current(c)
		c.JSON(http.StatusOK, gin.H{"authenticated": ok})
	})

	user := model.User{UUID: uuid.New(), Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	req := httptest.NewRequest(http.MethodGet, "/optional", nil)
	req.Header.Set("Authorization", "Bearer "+signedMiddlewareAuthTokenForTest(t, user))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"authenticated":false`) {
		t.Fatalf("expected anonymous response, got %s", w.Body.String())
	}
}
