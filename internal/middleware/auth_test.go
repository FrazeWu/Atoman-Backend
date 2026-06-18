package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/platform/authctx"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

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

	userID := uuid.New()
	claims := jwt.MapClaims{
		"user_id":  userID.String(),
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
	if !strings.Contains(body, userID.String()) {
		t.Fatalf("expected response body to include user ID, got %s", body)
	}
	if !strings.Contains(body, `"legacy_user_id":"`+userID.String()+`"`) {
		t.Fatalf("expected response body to include legacy user ID, got %s", body)
	}
}

func TestAuthMiddlewareFallsBackToSharedAuthCookieWhenAuthorizationHeaderIsInvalid(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
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

	userID := uuid.New()
	claims := jwt.MapClaims{
		"user_id":  userID.String(),
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
	if !strings.Contains(w.Body.String(), userID.String()) {
		t.Fatalf("expected response body to include cookie user ID, got %s", w.Body.String())
	}
}
