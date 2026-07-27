package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"atoman/internal/service"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func TestChangePasswordKeepsCurrentSessionAndRevokesOthers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("FRONTEND_URL", "https://www.atoman.org")
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{})
	oldHash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	user := model.User{Username: "password-user", Email: "password@example.com", Password: string(oldHash), Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	sessions := authsession.New(db)
	current, err := sessions.Create(user.UUID, authsession.KindWeb)
	if err != nil {
		t.Fatal(err)
	}
	other, err := sessions.Create(user.UUID, authsession.KindAPI)
	if err != nil {
		t.Fatal(err)
	}

	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	r := gin.New()
	r.PUT("/users/me/password", middleware.AuthMiddleware(), ChangePassword(db))
	req := httptest.NewRequest(http.MethodPut, "/users/me/password", bytes.NewBufferString(`{"current_password":"old-password","new_password":"new-password","password_confirm":"new-password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://www.atoman.org")
	req.Header.Set(middleware.CSRFHeaderName, current.CSRFToken)
	req.AddCookie(&http.Cookie{Name: middleware.AuthSessionCookieName, Value: current.Token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	var updated model.User
	if err := db.First(&updated, "uuid = ?", user.UUID).Error; err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte("new-password")); err != nil {
		t.Fatalf("new password was not saved: %v", err)
	}
	if _, err := sessions.Authenticate(current.Token, authsession.KindWeb); err != nil {
		t.Fatalf("current session should remain valid: %v", err)
	}
	if _, err := sessions.Authenticate(other.Token, authsession.KindAPI); !errors.Is(err, authsession.ErrInvalid) {
		t.Fatalf("other session should be revoked, got %v", err)
	}
}

func TestSetPasswordForOAuthOnlyAccountKeepsCurrentSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("FRONTEND_URL", "https://www.atoman.org")
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{})
	user := model.User{Username: "oauth-user", Email: "oauth@example.com", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	sessions := authsession.New(db)
	current, err := sessions.Create(user.UUID, authsession.KindWeb)
	if err != nil {
		t.Fatal(err)
	}
	other, err := sessions.Create(user.UUID, authsession.KindAPI)
	if err != nil {
		t.Fatal(err)
	}

	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	r := gin.New()
	r.POST("/users/me/password", middleware.AuthMiddleware(), SetPassword(db))
	req := httptest.NewRequest(http.MethodPost, "/users/me/password", bytes.NewBufferString(`{"password":"new-password","password_confirm":"new-password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://www.atoman.org")
	req.Header.Set(middleware.CSRFHeaderName, current.CSRFToken)
	req.AddCookie(&http.Cookie{Name: middleware.AuthSessionCookieName, Value: current.Token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	var updated model.User
	if err := db.First(&updated, "uuid = ?", user.UUID).Error; err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte("new-password")); err != nil {
		t.Fatalf("password was not saved: %v", err)
	}
	if _, err := sessions.Authenticate(current.Token, authsession.KindWeb); err != nil {
		t.Fatalf("current session should remain valid: %v", err)
	}
	if _, err := sessions.Authenticate(other.Token, authsession.KindAPI); !errors.Is(err, authsession.ErrInvalid) {
		t.Fatalf("other session should be revoked, got %v", err)
	}
}

func TestChangeEmailVerifiesCodeAndRevokesOtherSessions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("FRONTEND_URL", "https://www.atoman.org")
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{}, &model.EmailVerificationCode{})
	hash, err := bcrypt.GenerateFromPassword([]byte("current-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	user := model.User{Username: "email-user", Email: "old@example.com", Password: string(hash), Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	code, err := service.NewEmailServiceWithoutRedis(db).SendVerificationCodeForPurpose("new@example.com", service.VerificationPurposeEmailChange)
	if err != nil {
		t.Fatal(err)
	}
	sessions := authsession.New(db)
	current, err := sessions.Create(user.UUID, authsession.KindWeb)
	if err != nil {
		t.Fatal(err)
	}
	other, err := sessions.Create(user.UUID, authsession.KindAPI)
	if err != nil {
		t.Fatal(err)
	}

	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	r := gin.New()
	r.PUT("/users/me/email", middleware.AuthMiddleware(), ChangeEmail(db))
	req := httptest.NewRequest(http.MethodPut, "/users/me/email", bytes.NewBufferString(`{"email":"new@example.com","code":"`+code+`","current_password":"current-password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://www.atoman.org")
	req.Header.Set(middleware.CSRFHeaderName, current.CSRFToken)
	req.AddCookie(&http.Cookie{Name: middleware.AuthSessionCookieName, Value: current.Token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	var updated model.User
	if err := db.First(&updated, "uuid = ?", user.UUID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Email != "new@example.com" {
		t.Fatalf("expected updated email, got %q", updated.Email)
	}
	if _, err := sessions.Authenticate(other.Token, authsession.KindAPI); !errors.Is(err, authsession.ErrInvalid) {
		t.Fatalf("other session should be revoked, got %v", err)
	}
}

func TestRevokeSessionRejectsCurrentSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("FRONTEND_URL", "https://www.atoman.org")
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{})
	user := model.User{Username: "session-user", Email: "session-user@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	current, err := authsession.New(db).Create(user.UUID, authsession.KindWeb)
	if err != nil {
		t.Fatal(err)
	}
	var currentSession model.AuthSession
	if err := db.Where("token_hash = ?", authsession.Hash(current.Token)).First(&currentSession).Error; err != nil {
		t.Fatal(err)
	}
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	r := gin.New()
	r.DELETE("/users/me/sessions/:id", middleware.AuthMiddleware(), RevokeSession(db))
	req := httptest.NewRequest(http.MethodDelete, "/users/me/sessions/"+currentSession.ID.String(), nil)
	req.Header.Set("Origin", "https://www.atoman.org")
	req.Header.Set(middleware.CSRFHeaderName, current.CSRFToken)
	req.AddCookie(&http.Cookie{Name: middleware.AuthSessionCookieName, Value: current.Token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListSecurityActivitiesOnlyReturnsCurrentUsersAuthEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{}, &model.AuditLog{})
	user := model.User{Username: "activity-user", Email: "activity@example.com", Password: "hash", Role: "user", IsActive: true}
	other := model.User{Username: "other-activity-user", Email: "other-activity@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AuditLog{ActorID: &user.UUID, Action: "auth.password_changed", EntityType: "user", EntityID: &user.UUID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AuditLog{ActorID: &other.UUID, Action: "auth.password_changed", EntityType: "user", EntityID: &other.UUID}).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", user.UUID); c.Next() })
	r.GET("/users/me/security-activities", ListSecurityActivities(db))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/me/security-activities", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Activities []struct {
			Action string `json:"action"`
		} `json:"activities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Activities) != 1 || response.Activities[0].Action != "auth.password_changed" {
		t.Fatalf("unexpected activities: %#v", response.Activities)
	}
}

type userSettingsResponse struct {
	Data struct {
		PrivateProfile bool `json:"private_profile"`
	} `json:"data"`
	Error   string `json:"error"`
	Message string `json:"message"`
}

type searchUsersTestResponse struct {
	Data []struct {
		UUID     string `json:"uuid"`
		Username string `json:"username"`
	} `json:"data"`
}

func newUserSettingsTestRouter(t *testing.T) (*gin.Engine, *gorm.DB, model.User) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.UserSettings{})

	user := model.User{
		Username: "settings-user",
		Email:    "settings-user@example.com",
		Password: "hash",
		Role:     "user",
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", user.UUID)
		c.Next()
	})
	r.GET("/settings", GetUserSettings(db))
	r.PUT("/settings", UpdateUserSettings(db))
	return r, db, user
}

func decodeUserSettingsResponse(t *testing.T, body []byte) userSettingsResponse {
	t.Helper()

	var resp userSettingsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func installUserSettingsCreateError(t *testing.T, db *gorm.DB, createErr error) {
	t.Helper()

	callbackName := "user_settings_create_error_" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "UserSettings" {
			return
		}
		tx.AddError(createErr)
	}); err != nil {
		t.Fatalf("register create error callback: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Callback().Create().Remove(callbackName)
	})
}

func installUserSettingsCreateConflict(t *testing.T, db *gorm.DB, settings model.UserSettings) {
	t.Helper()

	if err := db.Create(&settings).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	callbackName := "user_settings_first_lookup_miss_" + strings.ReplaceAll(t.Name(), "/", "_")
	missed := false
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if missed || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "UserSettings" {
			return
		}
		missed = true
		tx.AddError(gorm.ErrRecordNotFound)
	}); err != nil {
		t.Fatalf("register first lookup miss callback: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(callbackName)
	})
}

func TestUpdateUserSettingsReturnsPersistedStateAfterInitialCreate(t *testing.T) {
	r, db, user := newUserSettingsTestRouter(t)

	body := bytes.NewBufferString(`{"private_profile":true}`)
	req := httptest.NewRequest(http.MethodPut, "/settings", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := decodeUserSettingsResponse(t, w.Body.Bytes())
	if !resp.Data.PrivateProfile {
		t.Fatalf("expected private_profile=true in response, got false")
	}

	var settings model.UserSettings
	if err := db.First(&settings, "user_id = ?", user.UUID).Error; err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if !settings.PrivateProfile {
		t.Fatalf("expected private_profile=true in db, got false")
	}
}

func TestUpdateUserProfileCanClearOptionalFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{})

	user := model.User{
		Username:    "profile-user",
		Email:       "profile-user@example.com",
		Password:    "hash",
		Role:        "user",
		IsActive:    true,
		DisplayName: "Display Name",
		AvatarURL:   "https://example.com/avatar.jpg",
		Bio:         "Bio",
		Website:     "https://example.com",
		Location:    "Berlin",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", user.UUID)
		c.Next()
	})
	r.PUT("/users/me", UpdateUserProfile(db))

	req := httptest.NewRequest(http.MethodPut, "/users/me", bytes.NewBufferString(`{"display_name":"","avatar_url":"","bio":"","website":"","location":""}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated model.User
	if err := db.First(&updated, "uuid = ?", user.UUID).Error; err != nil {
		t.Fatalf("load updated user: %v", err)
	}
	if updated.DisplayName != "" || updated.AvatarURL != "" || updated.Bio != "" || updated.Website != "" || updated.Location != "" {
		t.Fatalf("expected optional profile fields to be cleared, got %#v", updated)
	}
}

func TestSearchUsersMentionScopeReturnsAllActiveUsersWithPrefixFirst(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Follow{})

	currentUser := model.User{Username: "current", Email: "current@example.com", Password: "hash", Role: "user", IsActive: true}
	followedUser := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	followerUser := model.User{Username: "bob", Email: "bob@example.com", Password: "hash", Role: "user", IsActive: true}
	inactiveUser := model.User{Username: "adam-inactive", Email: "inactive@example.com", Password: "hash", Role: "user", IsActive: false}
	containsUser := model.User{Username: "z-alice", Email: "contains@example.com", Password: "hash", Role: "user", IsActive: true}
	for _, user := range []*model.User{&currentUser, &followedUser, &followerUser, &inactiveUser, &containsUser} {
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("create user %s: %v", user.Username, err)
		}
	}

	if err := db.Create(&[]model.Follow{
		{FollowerID: currentUser.UUID, FollowingID: followedUser.UUID},
		{FollowerID: followerUser.UUID, FollowingID: currentUser.UUID},
	}).Error; err != nil {
		t.Fatalf("create follows: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: currentUser.UUID, Username: currentUser.Username, Role: authctx.RoleUser})
		c.Next()
	})
	r.GET("/users/search", SearchUsers(db))

	req := httptest.NewRequest(http.MethodGet, "/users/search?scope=mention&q=ali&limit=10", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp searchUsersTestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 active mention results, got %d: %s", len(resp.Data), w.Body.String())
	}
	if resp.Data[0].UUID != followedUser.UUID.String() {
		t.Fatalf("expected username prefix result %s first, got %s: %s", followedUser.UUID, resp.Data[0].UUID, w.Body.String())
	}
	if resp.Data[1].UUID != containsUser.UUID.String() {
		t.Fatalf("expected non-followed contains result %s, got %s: %s", containsUser.UUID, resp.Data[1].UUID, w.Body.String())
	}
}

func TestSearchUsersMentionRequiresAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{})
	r := gin.New()
	r.GET("/users/search", SearchUsers(db))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/search?scope=mention", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"auth.unauthorized"`) {
		t.Fatalf("expected stable auth error, got %s", w.Body.String())
	}
}

func TestSearchUsersMentionRejectsInvalidLimitWhilePublicSearchKeepsFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{})
	user := model.User{Username: "searcher", Email: "searcher@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser})
		c.Next()
	})
	r.GET("/users/search", SearchUsers(db))
	for _, limit := range []string{"0", "-1", "abc"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/search?scope=mention&limit="+limit, nil))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"user.invalid_limit"`) {
			t.Fatalf("limit %s: expected stable 400, got %d: %s", limit, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/search?limit=abc", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("public fallback changed: %d: %s", w.Code, w.Body.String())
	}
}

func TestListUsersForRoleManagementScansCreatedAt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{})

	createdAt := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	user := model.User{
		Username:    "role-user",
		Email:       "role-user@example.com",
		Password:    "hash",
		Role:        "admin",
		DisplayName: "Role User",
		IsActive:    true,
		CreatedAt:   createdAt,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	r := gin.New()
	r.GET("/users/roles", ListUsersForRoleManagement(db))

	req := httptest.NewRequest(http.MethodGet, "/users/roles?limit=10", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			Username  string    `json:"username"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 user, got %d: %s", len(resp.Data), w.Body.String())
	}
	if resp.Data[0].Username != user.Username {
		t.Fatalf("expected username %q, got %q", user.Username, resp.Data[0].Username)
	}
	if resp.Data[0].CreatedAt.IsZero() {
		t.Fatalf("expected created_at in response, got zero time: %s", w.Body.String())
	}
}

func TestGetUserSettingsReturnsServerErrorWhenInitialCreateFails(t *testing.T) {
	r, db, _ := newUserSettingsTestRouter(t)
	installUserSettingsCreateError(t, db, errors.New("write failed"))

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}

	resp := decodeUserSettingsResponse(t, w.Body.Bytes())
	if resp.Error != "Failed to fetch settings" {
		t.Fatalf("expected fetch settings error, got %q", resp.Error)
	}

	var count int64
	if err := db.Model(&model.UserSettings{}).Count(&count).Error; err != nil {
		t.Fatalf("count settings: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no settings rows after failed create, got %d", count)
	}
}

func TestUpdateUserSettingsReturnsServerErrorWhenInitialCreateFails(t *testing.T) {
	r, db, _ := newUserSettingsTestRouter(t)
	installUserSettingsCreateError(t, db, errors.New("write failed"))

	body := bytes.NewBufferString(`{"private_profile":true}`)
	req := httptest.NewRequest(http.MethodPut, "/settings", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}

	resp := decodeUserSettingsResponse(t, w.Body.Bytes())
	if resp.Error != "Failed to update settings" {
		t.Fatalf("expected update settings error, got %q", resp.Error)
	}

	var count int64
	if err := db.Model(&model.UserSettings{}).Count(&count).Error; err != nil {
		t.Fatalf("count settings: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no settings rows after failed create, got %d", count)
	}
}

func TestGetUserSettingsHandlesInitialCreateConflictIdempotently(t *testing.T) {
	r, db, user := newUserSettingsTestRouter(t)

	expected := model.UserSettings{
		UserID:         user.UUID,
		PrivateProfile: true,
		DMPermission:   "one_before_reply",
	}
	installUserSettingsCreateConflict(t, db, expected)

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := decodeUserSettingsResponse(t, w.Body.Bytes())
	if !resp.Data.PrivateProfile {
		t.Fatalf("expected private_profile=true in response, got false")
	}

	var count int64
	if err := db.Model(&model.UserSettings{}).Where("user_id = ?", user.UUID).Count(&count).Error; err != nil {
		t.Fatalf("count settings: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 settings row, got %d", count)
	}
}

func TestSetupUserRoutesDoesNotRegisterLegacyBlogExplore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)

	r := gin.New()
	SetupUserRoutes(r, db)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/explore", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected legacy explore route to be absent, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUserBlockRoutesCreateListAndDeleteBlock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.UserBlock{})

	actor := model.User{Username: "blocker", Email: "blocker@example.com", Password: "hash", Role: "user", IsActive: true}
	target := model.User{Username: "blocked", Email: "blocked@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatalf("create actor: %v", err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", actor.UUID)
		c.Next()
	})
	r.GET("/users/blocked", ListBlockedUsers(db))
	r.POST("/users/:id/block", BlockUser(db))
	r.DELETE("/users/:id/block", UnblockUser(db))

	req := httptest.NewRequest(http.MethodPost, "/users/"+target.UUID.String()+"/block", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected block 200, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/users/blocked", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResponse struct {
		Data []struct {
			BlockedID string `json:"blocked_id"`
			Blocked   struct {
				Username string `json:"username"`
			} `json:"blocked"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("decode block list: %v", err)
	}
	if len(listResponse.Data) != 1 || listResponse.Data[0].BlockedID != target.UUID.String() || listResponse.Data[0].Blocked.Username != target.Username {
		t.Fatalf("unexpected block list: %s", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/users/"+target.UUID.String()+"/block", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected unblock 200, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	if err := db.Model(&model.UserBlock{}).Count(&count).Error; err != nil {
		t.Fatalf("count blocks: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no blocks after delete, got %d", count)
	}
}

func TestBlockUserRejectsSelf(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.UserBlock{})
	actor := model.User{Username: "self-blocker", Email: "self-blocker@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatalf("create actor: %v", err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", actor.UUID); c.Next() })
	r.POST("/users/:id/block", BlockUser(db))

	req := httptest.NewRequest(http.MethodPost, "/users/"+actor.UUID.String()+"/block", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected self block 400, got %d: %s", w.Code, w.Body.String())
	}
}
