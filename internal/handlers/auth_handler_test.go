package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type authErrorBody struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

func newAuthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.UserSettings{}); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return db
}

func signedAuthClaimsTokenForTest(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	if _, ok := claims["exp"]; !ok {
		claims["exp"] = time.Now().Add(time.Hour).Unix()
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func signedAuthTokenForTest(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	return signedAuthClaimsTokenForTest(t, jwt.MapClaims{
		"user_id":  userID.String(),
		"username": "missing-user",
		"role":     "user",
	})
}

func decodeAuthError(t *testing.T, body string) authErrorBody {
	t.Helper()
	var payload authErrorBody
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	return payload
}

func assertClearedAuthCookie(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()

	for _, cookie := range w.Result().Cookies() {
		if cookie.Name != authTokenCookieName {
			continue
		}
		if cookie.Value != "" {
			t.Fatalf("expected cleared auth cookie value to be empty, got %q", cookie.Value)
		}
		if cookie.MaxAge >= 0 {
			t.Fatalf("expected cleared auth cookie Max-Age to be negative, got %d", cookie.MaxAge)
		}
		return
	}

	t.Fatalf("expected cleared auth cookie, got none")
}

func TestSessionHandlerReturnsAuthRequiredWhenCookieMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	r := gin.New()
	r.GET("/session", SessionHandler(db))

	req := httptest.NewRequest(http.MethodGet, "/session", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	payload := decodeAuthError(t, w.Body.String())
	if payload.Code != "auth.required" || payload.Error != "请先登录" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if got := w.Header().Get("Set-Cookie"); got != "" {
		t.Fatalf("expected no cookie clearing, got %q", got)
	}
}

func TestSessionHandlerClearsCookieWhenTokenInvalid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	r := gin.New()
	r.GET("/session", SessionHandler(db))

	req := httptest.NewRequest(http.MethodGet, "/session", nil)
	req.AddCookie(&http.Cookie{Name: authTokenCookieName, Value: "not-a-jwt"})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	payload := decodeAuthError(t, w.Body.String())
	if payload.Code != "auth.invalid_token" || payload.Error != "登录状态已失效，请重新登录" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	assertClearedAuthCookie(t, w)
}

func TestSessionHandlerClearsCookieWhenClaimsMissingUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	r := gin.New()
	r.GET("/session", SessionHandler(db))

	req := httptest.NewRequest(http.MethodGet, "/session", nil)
	req.AddCookie(&http.Cookie{Name: authTokenCookieName, Value: signedAuthClaimsTokenForTest(t, jwt.MapClaims{
		"username": "missing-user",
		"role":     "user",
	})})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	payload := decodeAuthError(t, w.Body.String())
	if payload.Code != "auth.invalid_claims" || payload.Error != "登录信息异常，请重新登录" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	assertClearedAuthCookie(t, w)
}

func TestSessionHandlerClearsCookieWhenClaimsUserIDIsNotString(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	r := gin.New()
	r.GET("/session", SessionHandler(db))

	req := httptest.NewRequest(http.MethodGet, "/session", nil)
	req.AddCookie(&http.Cookie{Name: authTokenCookieName, Value: signedAuthClaimsTokenForTest(t, jwt.MapClaims{
		"user_id":  123,
		"username": "missing-user",
		"role":     "user",
	})})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	payload := decodeAuthError(t, w.Body.String())
	if payload.Code != "auth.invalid_claims" || payload.Error != "登录信息异常，请重新登录" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	assertClearedAuthCookie(t, w)
}

func TestSessionHandlerClearsCookieWhenTokenUserDoesNotExist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	r := gin.New()
	r.GET("/session", SessionHandler(db))

	req := httptest.NewRequest(http.MethodGet, "/session", nil)
	req.AddCookie(&http.Cookie{Name: authTokenCookieName, Value: signedAuthTokenForTest(t, uuid.New())})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	payload := decodeAuthError(t, w.Body.String())
	if payload.Code != "auth.user_not_found" || payload.Error != "账号不存在或已被移除，请重新登录" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	assertClearedAuthCookie(t, w)
}

func TestLoginHandlerReturnsAccountNotFoundCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	r := gin.New()
	r.POST("/login", LoginHandler(db))

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"nobody@example.com","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	payload := decodeAuthError(t, w.Body.String())
	if payload.Code != "auth.account_not_found" || payload.Error != "账号不存在" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestLoginHandlerReturnsPasswordMismatchCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.Create(&model.User{Username: "alice", Email: "alice@example.com", Password: string(hash), Role: "user"}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	r := gin.New()
	r.POST("/login", LoginHandler(db))

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"alice@example.com","password":"wrong-password"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	payload := decodeAuthError(t, w.Body.String())
	if payload.Code != "auth.password_mismatch" || payload.Error != "密码不正确" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestLoginHandlerReturnsTokenGenerationFailedCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "")
	db := newAuthTestDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.Create(&model.User{Username: "alice", Email: "alice@example.com", Password: string(hash), Role: "user"}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	r := gin.New()
	r.POST("/login", LoginHandler(db))

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"alice@example.com","password":"correct-password"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	payload := decodeAuthError(t, w.Body.String())
	if payload.Code != "auth.token_generation_failed" || payload.Error != "登录服务暂时不可用，请稍后重试" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}
