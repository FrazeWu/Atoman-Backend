package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/service"
	"atoman/internal/testdb"
	"github.com/gin-gonic/gin"
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
	db := testdb.Open(t)
	if err := db.AutoMigrate(
		&model.User{},
		&model.UserSettings{},
		&model.EmailVerificationCode{},
		&model.Channel{},
		&model.UserDefaultChannel{},
		&model.Collection{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
		&model.BookmarkFolder{},
		&model.Playlist{},
		&model.PlaylistSong{},
	); err != nil {
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

func TestSessionHandlerRejectsOutdatedAuthVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newAuthTestDB(t)
	user := model.User{
		Username: "reset-user", Email: "reset@example.com", Password: "hash", Role: "user", IsActive: true, AuthVersion: 1,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	token := signedAuthClaimsTokenForTest(t, jwt.MapClaims{
		"user_id": user.UUID.String(), "username": user.Username, "role": user.Role, "auth_version": 0,
	})
	r := gin.New()
	r.GET("/session", SessionHandler(db))
	req := httptest.NewRequest(http.MethodGet, "/session", nil)
	req.AddCookie(&http.Cookie{Name: authTokenCookieName, Value: token})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
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

func TestSendVerificationHandlerRequiresTurnstileInProduction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "production")
	t.Setenv("GIN_MODE", gin.ReleaseMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "configured-secret")

	db := newAuthTestDB(t)
	r := gin.New()
	r.POST("/send-verification", SendVerificationHandler(service.NewEmailServiceWithoutRedis(db)))

	req := httptest.NewRequest(http.MethodPost, "/send-verification", strings.NewReader(`{"email":"protected@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without Turnstile token, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPasswordResetSendCodeHidesAccountExistence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "development")
	t.Setenv("GIN_MODE", gin.DebugMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	t.Setenv("RESEND_API_KEY", "")
	db := newAuthTestDB(t)
	if err := db.Create(&model.User{
		Username: "reset-user", Email: "reset@example.com", Password: "hash", Role: "user", IsActive: true,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	r := gin.New()
	SetupAuthRoutes(r, db, service.NewEmailServiceWithoutRedis(db))

	request := func(email string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password-reset/send-code", strings.NewReader(`{"email":"`+email+`"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	known := request("RESET@EXAMPLE.COM")
	unknown := request("missing@example.com")
	if known.Code != http.StatusOK || unknown.Code != http.StatusOK {
		t.Fatalf("expected matching 200 responses, got known=%d unknown=%d", known.Code, unknown.Code)
	}
	if known.Body.String() != unknown.Body.String() {
		t.Fatalf("expected matching responses, got known=%q unknown=%q", known.Body.String(), unknown.Body.String())
	}

	var resetCode model.EmailVerificationCode
	if err := db.First(&resetCode, "email = ? AND purpose = ?", "reset@example.com", service.VerificationPurposePasswordReset).Error; err != nil {
		t.Fatalf("load password reset code: %v", err)
	}
	var unknownCount int64
	if err := db.Model(&model.EmailVerificationCode{}).Where("email = ?", "missing@example.com").Count(&unknownCount).Error; err != nil {
		t.Fatalf("count unknown email codes: %v", err)
	}
	if unknownCount != 0 {
		t.Fatalf("expected no code for unknown email, got %d", unknownCount)
	}
}

func TestPasswordResetUpdatesPasswordAndAuthVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "development")
	t.Setenv("GIN_MODE", gin.DebugMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	db := newAuthTestDB(t)
	oldHash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash old password: %v", err)
	}
	user := model.User{Username: "reset-user", Email: "reset@example.com", Password: string(oldHash), Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	code := model.EmailVerificationCode{
		Email: "reset@example.com", Purpose: service.VerificationPurposePasswordReset,
		Code: "123456", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := db.Create(&code).Error; err != nil {
		t.Fatalf("create reset code: %v", err)
	}
	r := gin.New()
	SetupAuthRoutes(r, db, service.NewEmailServiceWithoutRedis(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password-reset", strings.NewReader(`{"email":"RESET@example.com","code":"123456","password":"new-password","password_confirm":"new-password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: authTokenCookieName, Value: "old-token"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated model.User
	if err := db.First(&updated, "uuid = ?", user.UUID).Error; err != nil {
		t.Fatalf("load updated user: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte("new-password")); err != nil {
		t.Fatalf("new password was not stored: %v", err)
	}
	if updated.AuthVersion != 1 {
		t.Fatalf("expected auth version 1, got %d", updated.AuthVersion)
	}
	var consumed model.EmailVerificationCode
	if err := db.First(&consumed, "uuid = ?", code.UUID).Error; err != nil {
		t.Fatalf("load reset code: %v", err)
	}
	if !consumed.Used {
		t.Fatal("expected reset code to be consumed")
	}
	assertClearedAuthCookie(t, w)
}

func TestPasswordResetRejectsRegistrationCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "development")
	t.Setenv("GIN_MODE", gin.DebugMode)
	db := newAuthTestDB(t)
	user := model.User{Username: "reset-user", Email: "reset@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	seedAuthVerificationCode(t, db, user.Email)
	r := gin.New()
	SetupAuthRoutes(r, db, service.NewEmailServiceWithoutRedis(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password-reset", strings.NewReader(`{"email":"reset@example.com","code":"123456","password":"new-password","password_confirm":"new-password"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
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

func TestCheckEmailHandlerReportsRegisteredEmail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAuthTestDB(t)
	if err := db.Create(&model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	r := gin.New()
	r.POST("/check-email", CheckEmailHandler(db))

	req := httptest.NewRequest(http.MethodPost, "/check-email", strings.NewReader(`{"email":"alice@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"available":false`) || !strings.Contains(w.Body.String(), `"reason":"registered"`) {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestCheckUsernameHandlerReportsTakenUsername(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAuthTestDB(t)
	owner := model.User{Username: "owner", Email: "owner@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channel := model.Channel{Name: "Design", Slug: "design", UserID: &owner.UUID}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	r := gin.New()
	r.POST("/check-username", CheckUsernameHandler(db))

	req := httptest.NewRequest(http.MethodPost, "/check-username", strings.NewReader(`{"username":"design"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"available":false`) || !strings.Contains(w.Body.String(), `"reason":"taken"`) {
		t.Fatalf("unexpected body: %s", w.Body.String())
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

func TestRegisterHandlerCreatesDefaultBootstrapResources(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "development")
	t.Setenv("GIN_MODE", gin.DebugMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	t.Setenv("JWT_SECRET", "test-secret")

	db := newAuthTestDB(t)
	email := "bootstrap-user@example.com"
	seedAuthVerificationCode(t, db, email)

	r := gin.New()
	r.POST("/register", RegisterHandler(db, service.NewEmailServiceWithoutRedis(db)))

	body := `{"username":"bootstrap","email":"` + email + `","password":"secret123","password_confirm":"secret123","verification_code":"123456"}`
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var user model.User
	if err := db.Where("email = ?", email).First(&user).Error; err != nil {
		t.Fatalf("find created user: %v", err)
	}

	var channels []model.Channel
	if err := db.Where("user_id = ?", user.UUID).Find(&channels).Error; err != nil {
		t.Fatalf("find default channels: %v", err)
	}
	if len(channels) != 3 {
		t.Fatalf("expected three module channels, got %d", len(channels))
	}
	for _, contentType := range []string{model.ChannelContentTypeBlog, model.ChannelContentTypePodcast, model.ChannelContentTypeVideo} {
		var selection model.UserDefaultChannel
		if err := db.Preload("Channel").Where("user_id = ? AND content_type = ?", user.UUID, contentType).First(&selection).Error; err != nil {
			t.Fatalf("find %s default channel selection: %v", contentType, err)
		}
		if selection.Channel == nil || selection.Channel.ContentType != contentType {
			t.Fatalf("unexpected %s default channel: %#v", contentType, selection.Channel)
		}
		var collection model.Collection
		if err := db.Where("channel_id = ? AND is_default = ?", selection.ChannelID, true).First(&collection).Error; err != nil {
			t.Fatalf("find %s default collection: %v", contentType, err)
		}
	}

	var groups []model.SubscriptionGroup
	if err := db.Where("user_id = ? AND name = ?", user.UUID, "默认分组").Find(&groups).Error; err != nil {
		t.Fatalf("find default subscription groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected one default subscription group, got %d", len(groups))
	}

	var subscriptions []model.Subscription
	if err := db.Preload("FeedSource").Where("user_id = ?", user.UUID).Find(&subscriptions).Error; err != nil {
		t.Fatalf("find subscriptions: %v", err)
	}
	if len(subscriptions) != 1 {
		t.Fatalf("expected one auto subscription, got %d", len(subscriptions))
	}
	if subscriptions[0].FeedSource == nil {
		t.Fatalf("expected feed source to be preloaded")
	}
	if subscriptions[0].FeedSource.SourceType != "internal_user" {
		t.Fatalf("expected internal_user subscription, got %s", subscriptions[0].FeedSource.SourceType)
	}
	if subscriptions[0].FeedSource.SourceID == nil || *subscriptions[0].FeedSource.SourceID != user.UUID {
		t.Fatalf("expected subscription source id to match user uuid")
	}

	var folders []model.BookmarkFolder
	if err := db.Where("user_id = ? AND name = ?", user.UUID, "默认收藏夹").Find(&folders).Error; err != nil {
		t.Fatalf("find bookmark folders: %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("expected one default bookmark folder, got %d", len(folders))
	}

	var playlists []model.Playlist
	if err := db.Where("user_id = ? AND name = ?", user.UUID, "最爱").Find(&playlists).Error; err != nil {
		t.Fatalf("find favorite playlists: %v", err)
	}
	if len(playlists) != 1 {
		t.Fatalf("expected one default favorite playlist, got %d", len(playlists))
	}
	if !playlists[0].IsFavorite || playlists[0].IsPublic {
		t.Fatalf("expected private system favorite playlist, got %#v", playlists[0])
	}

	var playlistSongs int64
	if err := db.Model(&model.PlaylistSong{}).Where("playlist_id = ?", playlists[0].ID).Count(&playlistSongs).Error; err != nil {
		t.Fatalf("count playlist songs: %v", err)
	}
	if playlistSongs != 0 {
		t.Fatalf("expected empty favorite playlist, got %d songs", playlistSongs)
	}
}

func TestRegisterHandlerDoesNotRequireSecondTurnstileVerification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "production")
	t.Setenv("GIN_MODE", gin.ReleaseMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "configured-secret")
	t.Setenv("JWT_SECRET", "test-secret")

	db := newAuthTestDB(t)
	email := "single-turnstile@example.com"
	seedAuthVerificationCode(t, db, email)

	r := gin.New()
	r.POST("/register", RegisterHandler(db, service.NewEmailServiceWithoutRedis(db)))

	body := `{"username":"singleturnstile","email":"` + email + `","password":"secret123","password_confirm":"secret123","verification_code":"123456"}`
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 without a second Turnstile token, got %d: %s", w.Code, w.Body.String())
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
