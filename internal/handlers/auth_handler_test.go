package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authsession"
	"atoman/internal/service"
	"atoman/internal/testdb"
	"github.com/gin-gonic/gin"
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
		&model.AuthSession{},
		&model.LoginEvent{},
		&model.ExternalIdentity{},
		&model.AuditLog{},
		&model.EmailVerificationCode{},
		&model.Channel{},
		&model.Collection{},
		&model.UserStudioState{},
		&model.StudioModuleSettings{},
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

func TestLoginHandlerCreatesWebSessionWithoutReturningToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAuthTestDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := model.User{Username: "web-user", Email: "web@example.com", Password: string(hash), Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	r := gin.New()
	r.POST("/login", LoginHandler(db))
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"WEB@example.com","password":"correct-password"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"token"`) || !strings.Contains(w.Body.String(), `"csrf_token"`) {
		t.Fatalf("web response must contain csrf but no token: %s", w.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == middleware.AuthSessionCookieName {
			sessionCookie = cookie
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" || !sessionCookie.HttpOnly {
		t.Fatalf("expected HttpOnly web session cookie, got %#v", sessionCookie)
	}
	var stored model.AuthSession
	if err := db.First(&stored, "user_id = ?", user.UUID).Error; err != nil {
		t.Fatalf("load auth session: %v", err)
	}
	if stored.Kind != authsession.KindWeb || stored.TokenHash == sessionCookie.Value {
		t.Fatalf("unexpected stored session: %#v", stored)
	}
}

func TestTokenLoginHandlerCreatesAPISessionWithoutCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAuthTestDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := model.User{Username: "api-user", Email: "api@example.com", Password: string(hash), Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	r := gin.New()
	r.POST("/token", TokenLoginHandler(db))
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(`{"username":"api-user","password":"correct-password"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"token"`) || strings.Contains(w.Body.String(), `"csrf_token"`) {
		t.Fatalf("unexpected token response %d: %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatalf("api token login must not set cookies: %#v", w.Result().Cookies())
	}
	var stored model.AuthSession
	if err := db.First(&stored, "user_id = ?", user.UUID).Error; err != nil {
		t.Fatalf("load auth session: %v", err)
	}
	if stored.Kind != authsession.KindAPI {
		t.Fatalf("expected api session, got %#v", stored)
	}
}

func TestSessionHandlerRotatesCSRFAndLogoutRevokesSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("FRONTEND_URL", "https://www.atoman.org")
	db := newAuthTestDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := model.User{Username: "session-user", Email: "session@example.com", Password: string(hash), Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	credentials, err := authsession.New(db).Create(user.UUID, authsession.KindWeb)
	if err != nil {
		t.Fatalf("create web session: %v", err)
	}
	cookie := &http.Cookie{Name: middleware.AuthSessionCookieName, Value: credentials.Token}

	sessionRouter := gin.New()
	sessionRouter.GET("/session", SessionHandler(db))
	sessionRequest := httptest.NewRequest(http.MethodGet, "/session", nil)
	sessionRequest.AddCookie(cookie)
	sessionResponse := httptest.NewRecorder()
	sessionRouter.ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusOK {
		t.Fatalf("expected session restore, got %d: %s", sessionResponse.Code, sessionResponse.Body.String())
	}
	var payload struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(sessionResponse.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if payload.CSRFToken == "" || payload.CSRFToken == credentials.CSRFToken {
		t.Fatalf("expected rotated csrf token, got %q", payload.CSRFToken)
	}

	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	logoutRouter := gin.New()
	logoutRouter.POST("/logout", middleware.AuthMiddleware(), LogoutHandler(db))
	logoutRequest := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutRequest.AddCookie(cookie)
	logoutRequest.Header.Set("Origin", "https://www.atoman.org")
	logoutRequest.Header.Set(middleware.CSRFHeaderName, payload.CSRFToken)
	logoutResponse := httptest.NewRecorder()
	logoutRouter.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("expected logout 204, got %d: %s", logoutResponse.Code, logoutResponse.Body.String())
	}
	if _, err := authsession.New(db).Authenticate(credentials.Token, authsession.KindWeb); !errors.Is(err, authsession.ErrInvalid) {
		t.Fatalf("expected session revoked after logout, got %v", err)
	}
	assertClearedAuthCookie(t, logoutResponse)
}

func seedAuthVerificationCode(t *testing.T, db *gorm.DB, email string) string {
	t.Helper()
	t.Setenv("AUTH_CODE_SECRET", "test-auth-code-secret")
	code, err := service.NewEmailServiceWithoutRedis(db).SendVerificationCode(email)
	if err != nil {
		t.Fatalf("seed verification code: %v", err)
	}
	return code
}

func apiAuthTokenForTest(t *testing.T, db *gorm.DB, user model.User) string {
	t.Helper()
	credentials, err := authsession.New(db).Create(user.UUID, authsession.KindAPI)
	if err != nil {
		t.Fatalf("create api auth session: %v", err)
	}
	return credentials.Token
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

func TestLoginHandlerReturnsAccountNotFoundCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
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
	db := newAuthTestDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := model.User{Username: "alice", Email: "alice@example.com", Password: string(hash), Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	r := gin.New()
	r.POST("/login", LoginHandler(db))

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"Alice@Example.com","password":"correct-password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Safari/605.1.15")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"username":"alice"`) {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
	var event model.LoginEvent
	if err := db.Last(&event, "user_id = ?", user.UUID).Error; err != nil {
		t.Fatalf("load login event: %v", err)
	}
	if event.Result != model.LoginResultSucceeded || event.Method != "password" || event.IPAddress != "192.0.2.1" || event.UserAgent == "" || event.SessionID == nil {
		t.Fatalf("unexpected login event: %#v", event)
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
	if stored.CodeHash == "" {
		t.Fatal("expected verification code hash to be stored")
	}
	if strings.Contains(stderr, stored.CodeHash) || strings.Contains(stderr, email) {
		t.Fatalf("expected stderr not to contain verification secrets, got %q", stderr)
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
	oldSession, err := authsession.New(db).Create(user.UUID, authsession.KindAPI)
	if err != nil {
		t.Fatalf("create old api session: %v", err)
	}
	emailService := service.NewEmailServiceWithoutRedis(db)
	resetCode, err := emailService.SendVerificationCodeForPurpose("reset@example.com", service.VerificationPurposePasswordReset)
	if err != nil {
		t.Fatalf("create reset code: %v", err)
	}
	r := gin.New()
	SetupAuthRoutes(r, db, service.NewEmailServiceWithoutRedis(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password-reset", strings.NewReader(`{"email":"RESET@example.com","code":"`+resetCode+`","password":"new-password","password_confirm":"new-password"}`))
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
	if err := db.First(&consumed, "email = ? AND purpose = ?", user.Email, service.VerificationPurposePasswordReset).Error; err != nil {
		t.Fatalf("load reset code: %v", err)
	}
	if !consumed.Used {
		t.Fatal("expected reset code to be consumed")
	}
	if _, err := authsession.New(db).Authenticate(oldSession.Token, authsession.KindAPI); !errors.Is(err, authsession.ErrInvalid) {
		t.Fatalf("expected password reset to revoke existing sessions, got %v", err)
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
	registrationCode := seedAuthVerificationCode(t, db, user.Email)
	r := gin.New()
	SetupAuthRoutes(r, db, service.NewEmailServiceWithoutRedis(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password-reset", strings.NewReader(`{"email":"reset@example.com","code":"`+registrationCode+`","password":"new-password","password_confirm":"new-password"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPasswordResetRejectsPasswordOver72Bytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAuthTestDB(t)
	r := gin.New()
	r.POST("/password-reset", PasswordResetHandler(db))
	body := `{"email":"reset@example.com","code":"123456","password":"` + strings.Repeat("a", 73) + `","password_confirm":"` + strings.Repeat("a", 73) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/password-reset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for password over 72 bytes, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterHandlerRejectsReservedUsername(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "development")
	t.Setenv("GIN_MODE", gin.DebugMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	db := newAuthTestDB(t)
	email := "feed-user@example.com"
	code := seedAuthVerificationCode(t, db, email)

	r := gin.New()
	r.POST("/register", RegisterHandler(db, service.NewEmailServiceWithoutRedis(db)))
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{"username":"feed","email":"`+email+`","password":"secret123","password_confirm":"secret123","verification_code":"`+code+`"}`))
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

func TestDeletedAccountUsernameAndEmailRemainUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "development")
	t.Setenv("GIN_MODE", gin.DebugMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	db := newAuthTestDB(t)
	deleted := model.User{Username: "retired-user", Email: "retired@example.com", Password: "hash", Role: "user", IsActive: false}
	if err := db.Create(&deleted).Error; err != nil {
		t.Fatalf("create deleted user: %v", err)
	}
	if err := db.Delete(&deleted).Error; err != nil {
		t.Fatalf("soft delete user: %v", err)
	}

	r := gin.New()
	r.POST("/check-email", CheckEmailHandler(db))
	r.POST("/check-username", CheckUsernameHandler(db))
	r.POST("/register", RegisterHandler(db, service.NewEmailServiceWithoutRedis(db)))

	for _, check := range []struct {
		path string
		body string
	}{
		{path: "/check-email", body: `{"email":"RETIRED@example.com"}`},
		{path: "/check-username", body: `{"username":"retired-user"}`},
	} {
		req := httptest.NewRequest(http.MethodPost, check.path, strings.NewReader(check.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"available":false`) {
			t.Fatalf("expected %s to report unavailable, got %d: %s", check.path, w.Code, w.Body.String())
		}
	}

	code := seedAuthVerificationCode(t, db, deleted.Email)
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{"username":"retired-user","email":"retired@example.com","password":"secret123","password_confirm":"secret123","verification_code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected registration to reject occupied identity, got %d: %s", w.Code, w.Body.String())
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
	code := seedAuthVerificationCode(t, db, email)

	r := gin.New()
	r.POST("/register", RegisterHandler(db, service.NewEmailServiceWithoutRedis(db)))
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{"username":"design","email":"`+email+`","password":"secret123","password_confirm":"secret123","verification_code":"`+code+`"}`))
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

	db := newAuthTestDB(t)
	email := "bootstrap-user@example.com"
	code := seedAuthVerificationCode(t, db, email)

	r := gin.New()
	r.POST("/register", RegisterHandler(db, service.NewEmailServiceWithoutRedis(db)))

	body := `{"username":"bootstrap","email":"` + email + `","password":"secret123","password_confirm":"secret123","verification_code":"` + code + `"}`
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"token"`) || !strings.Contains(w.Body.String(), `"csrf_token"`) {
		t.Fatalf("registration must return csrf but no token: %s", w.Body.String())
	}
	foundSessionCookie := false
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == middleware.AuthSessionCookieName && cookie.Value != "" {
			foundSessionCookie = true
		}
	}
	if !foundSessionCookie {
		t.Fatal("expected registration to create a web session cookie")
	}

	var user model.User
	if err := db.Where("email = ?", email).First(&user).Error; err != nil {
		t.Fatalf("find created user: %v", err)
	}
	var webSession model.AuthSession
	if err := db.First(&webSession, "user_id = ? AND kind = ?", user.UUID, authsession.KindWeb).Error; err != nil {
		t.Fatalf("find registration web session: %v", err)
	}

	var channels []model.Channel
	if err := db.Where("user_id = ?", user.UUID).Find(&channels).Error; err != nil {
		t.Fatalf("find default channels: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("expected one unified studio channel, got %d", len(channels))
	}
	channel := channels[0]
	var state model.UserStudioState
	if err := db.First(&state, "user_id = ?", user.UUID).Error; err != nil {
		t.Fatalf("find current studio channel: %v", err)
	}
	if state.ChannelID == nil || *state.ChannelID != channel.ID {
		t.Fatalf("expected current channel %s, got %#v", channel.ID, state.ChannelID)
	}
	for _, contentType := range []string{"blog", "podcast", "video"} {
		var collection model.Collection
		if err := db.Where("channel_id = ? AND content_type = ? AND is_default = ?", channel.ID, contentType, true).First(&collection).Error; err != nil {
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

	var playlists int64
	if err := db.Model(&model.Playlist{}).Where("user_id = ?", user.UUID).Count(&playlists).Error; err != nil {
		t.Fatalf("count default music playlists: %v", err)
	}
	if playlists != 0 {
		t.Fatalf("expected music playlists to be created on demand, got %d playlists", playlists)
	}
}

func TestRegisterHandlerDoesNotRequireSecondTurnstileVerification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ENV", "production")
	t.Setenv("GIN_MODE", gin.ReleaseMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "configured-secret")

	db := newAuthTestDB(t)
	email := "single-turnstile@example.com"
	code := seedAuthVerificationCode(t, db, email)

	r := gin.New()
	r.POST("/register", RegisterHandler(db, service.NewEmailServiceWithoutRedis(db)))

	body := `{"username":"singleturnstile","email":"` + email + `","password":"secret123","password_confirm":"secret123","verification_code":"` + code + `"}`
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

func TestLoginHandlerRateLimitsRepeatedAccountFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAuthTestDB(t)
	r := gin.New()
	r.POST("/login", LoginHandler(db))
	for attempt := 1; attempt <= 11; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"limited@example.com","password":"wrong-password"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if attempt <= 10 && w.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d was limited too early", attempt)
		}
		if attempt == 11 {
			if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
				t.Fatalf("expected rate limit with Retry-After, got %d: %s", w.Code, w.Body.String())
			}
		}
	}
}

func TestLoginHandlerReturnsPasswordNotSetForOAuthOnlyAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAuthTestDB(t)
	if err := db.Create(&model.User{Username: "oauth-only", Email: "oauth-only@example.com", Role: "user"}).Error; err != nil {
		t.Fatalf("create oauth-only user: %v", err)
	}
	r := gin.New()
	r.POST("/login", LoginHandler(db))

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"oauth-only@example.com","password":"any-password"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	payload := decodeAuthError(t, w.Body.String())
	if payload.Code != "auth.password_not_set" || payload.Error != "请使用第三方账号登录" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}
