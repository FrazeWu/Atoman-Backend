package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/service"
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
	if err := db.AutoMigrate(&model.User{}, &model.UserSettings{}, &model.EmailVerificationCode{}, &model.Channel{}); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return db
}

func seedAuthVerificationCode(t *testing.T, db *gorm.DB, email string) {
	t.Helper()

	code := model.EmailVerificationCode{
		Email:     email,
		Code:      "123456",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
		Used:      false,
	}
	if err := db.Create(&code).Error; err != nil {
		t.Fatalf("seed verification code: %v", err)
	}
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

func captureAuthHandlerStderr(t *testing.T, fn func()) string {
	t.Helper()

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}

	os.Stderr = w
	defer func() {
		os.Stderr = oldStderr
	}()

	outputCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		outputCh <- buf.String()
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close stderr pipe: %v", err)
	}

	return <-outputCh
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

func TestSessionHandlerReturnsNoContentWhenCookieMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	r := gin.New()
	r.GET("/session", SessionHandler(db))

	req := httptest.NewRequest(http.MethodGet, "/session", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); body != "" {
		t.Fatalf("expected empty body, got %q", body)
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

func TestSessionHandlerClearsCookieWhenTokenUserIsInactive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	inactive := model.User{Username: "inactive", Email: "inactive@example.com", Password: "hash", Role: "user", IsActive: false}
	if err := db.Create(&inactive).Error; err != nil {
		t.Fatalf("create inactive user: %v", err)
	}
	if err := db.Model(&inactive).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate user: %v", err)
	}
	r := gin.New()
	r.GET("/session", SessionHandler(db))

	req := httptest.NewRequest(http.MethodGet, "/session", nil)
	req.AddCookie(&http.Cookie{Name: authTokenCookieName, Value: signedAuthTokenForTest(t, inactive.UUID)})
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

func TestLoginHandlerRejectsInactiveUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.Create(&model.User{Username: "inactive", Email: "inactive@example.com", Password: string(hash), Role: "user", IsActive: false}).Error; err != nil {
		t.Fatalf("create inactive user: %v", err)
	}
	if err := db.Model(&model.User{}).Where("email = ?", "inactive@example.com").Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate user: %v", err)
	}
	r := gin.New()
	r.POST("/login", LoginHandler(db))

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"inactive@example.com","password":"correct-password"}`))
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

func TestLoginHandlerAcceptsEmailCaseInsensitively(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.Create(&model.User{Username: "alice", Email: "alice@example.com", Password: string(hash), Role: "user", IsActive: true}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	r := gin.New()
	r.POST("/login", LoginHandler(db))

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"Alice@Example.com","password":"correct-password"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"username":"alice"`) {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestSendVerificationHandlerDoesNotLogVerificationCode(t *testing.T) {
	gin.SetMode(gin.DebugMode)
	t.Setenv("GIN_MODE", gin.DebugMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	t.Setenv("RESEND_API_KEY", "")
	db := newAuthTestDB(t)
	email := "debug-leak@example.com"

	r := gin.New()
	r.POST("/send-verification", SendVerificationHandler(service.NewEmailServiceWithoutRedis(db)))

	req := httptest.NewRequest(http.MethodPost, "/send-verification", strings.NewReader(`{"email":"`+email+`","turnstile_token":""}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	stderr := captureAuthHandlerStderr(t, func() {
		r.ServeHTTP(w, req)
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var stored model.EmailVerificationCode
	if err := db.Where("email = ?", email).First(&stored).Error; err != nil {
		t.Fatalf("load verification code: %v", err)
	}
	if stored.Code == "" {
		t.Fatal("expected verification code to be stored")
	}
	if strings.Contains(stderr, stored.Code) {
		t.Fatalf("expected stderr not to contain verification code %q, got %q", stored.Code, stderr)
	}
}

func TestRegisterHandlerRejectsReservedUsername(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "development")
	t.Setenv("GIN_MODE", gin.DebugMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	email := "feed-user@example.com"
	seedAuthVerificationCode(t, db, email)

	r := gin.New()
	r.POST("/register", RegisterHandler(db, service.NewEmailServiceWithoutRedis(db)))
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{"username":"feed","email":"`+email+`","password":"secret123","password_confirm":"secret123","verification_code":"123456"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "reserved") {
		t.Fatalf("expected reserved error, got %s", w.Body.String())
	}
}

func TestRegisterHandlerRejectsUsernameMatchingChannelSlug(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "development")
	t.Setenv("GIN_MODE", gin.DebugMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	owner := model.User{Username: "owner", Email: "owner@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channel := model.Channel{Name: "Design", Slug: "design", UserID: &owner.UUID}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	email := "design-user@example.com"
	seedAuthVerificationCode(t, db, email)

	r := gin.New()
	r.POST("/register", RegisterHandler(db, service.NewEmailServiceWithoutRedis(db)))
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{"username":"design","email":"`+email+`","password":"secret123","password_confirm":"secret123","verification_code":"123456"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "already in use") {
		t.Fatalf("expected already in use error, got %s", w.Body.String())
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
