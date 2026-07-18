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

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type userSettingsResponse struct {
	Data    model.UserSettings `json:"data"`
	Error   string             `json:"error"`
	Message string             `json:"message"`
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

	body := bytes.NewBufferString(`{"private_profile":true,"dm_permission":"following_only"}`)
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
	if resp.Data.DMPermission != "following_only" {
		t.Fatalf("expected dm_permission=following_only in response, got %q", resp.Data.DMPermission)
	}

	var settings model.UserSettings
	if err := db.First(&settings, "user_id = ?", user.UUID).Error; err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if !settings.PrivateProfile {
		t.Fatalf("expected private_profile=true in db, got false")
	}
	if settings.DMPermission != "following_only" {
		t.Fatalf("expected dm_permission=following_only in db, got %q", settings.DMPermission)
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
	if resp.Data.UserID != user.UUID {
		t.Fatalf("expected response user_id=%s, got %s", user.UUID, resp.Data.UserID)
	}
	if !resp.Data.PrivateProfile {
		t.Fatalf("expected private_profile=true in response, got false")
	}
	if resp.Data.DMPermission != "one_before_reply" {
		t.Fatalf("expected dm_permission=one_before_reply in response, got %q", resp.Data.DMPermission)
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
