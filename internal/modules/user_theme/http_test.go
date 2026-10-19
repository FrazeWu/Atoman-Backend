package user_theme

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func newTestEnv(t *testing.T) (*Service, *Handler, *gorm.DB, model.User) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{})
	if err := migrations.RunUserThemesMigration(db); err != nil {
		t.Fatalf("run user themes migration: %v", err)
	}

	user := model.User{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "hash",
		Role:     authctx.RoleUser,
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	svc := NewService(db)
	handler := NewHandler(svc)
	return svc, handler, db, user
}

func newTestRouter(handler *Handler, currentUser *authctx.CurrentUser) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if currentUser != nil {
			authctx.SetCurrentUser(c, *currentUser)
		}
		c.Next()
	})
	v1 := r.Group("/api/v1")
	RegisterRoutes(v1, handler)
	return r
}

func themeRequest(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestGetTheme_UserNotFound(t *testing.T) {
	_, handler, _, _ := newTestEnv(t)
	r := newTestRouter(handler, nil)

	w := themeRequest(t, r, http.MethodGet, "/api/v1/users/nonexistent/theme", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"user.not_found"`) {
		t.Fatalf("expected user.not_found error code, got: %s", w.Body.String())
	}
}

func TestGetTheme_DefaultThemeWhenNotSet(t *testing.T) {
	_, handler, _, user := newTestEnv(t)
	r := newTestRouter(handler, nil)

	// Lookup by username
	w := themeRequest(t, r, http.MethodGet, "/api/v1/users/alice/theme", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data UserThemeDTO `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Data.IsEnabled {
		t.Errorf("expected is_enabled to be false, got true")
	}
	if resp.Data.Mode != "preset" {
		t.Errorf("expected mode to be 'preset', got %q", resp.Data.Mode)
	}

	// Lookup by UUID
	wUUID := themeRequest(t, r, http.MethodGet, "/api/v1/users/"+user.UUID.String()+"/theme", "")
	if wUUID.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wUUID.Code, wUUID.Body.String())
	}
}

func TestPutTheme_Unauthorized(t *testing.T) {
	_, handler, _, _ := newTestEnv(t)
	r := newTestRouter(handler, nil)

	w := themeRequest(t, r, http.MethodPut, "/api/v1/users/me/theme", `{"mode":"preset"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPutTheme_UpdatePresetTheme(t *testing.T) {
	_, handler, _, user := newTestEnv(t)
	cur := authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
	r := newTestRouter(handler, &cur)

	payload := `{
		"is_enabled": true,
		"mode": "preset",
		"preset_config": {"primary_color": "#ff6600", "font": "sans-serif"}
	}`
	w := themeRequest(t, r, http.MethodPut, "/api/v1/users/me/theme", payload)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data UserThemeDTO `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.Data.IsEnabled {
		t.Errorf("expected is_enabled to be true")
	}
	if resp.Data.Mode != "preset" {
		t.Errorf("expected mode 'preset', got %s", resp.Data.Mode)
	}
	if !strings.Contains(string(resp.Data.PresetConfig), "#ff6600") {
		t.Errorf("expected preset_config to contain #ff6600, got %s", string(resp.Data.PresetConfig))
	}

	// Verify public GET returns this updated theme
	wGet := themeRequest(t, r, http.MethodGet, "/api/v1/users/alice/theme", "")
	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200 on GET, got %d: %s", wGet.Code, wGet.Body.String())
	}
	var getResp struct {
		Data UserThemeDTO `json:"data"`
	}
	if err := json.Unmarshal(wGet.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if !getResp.Data.IsEnabled || getResp.Data.Mode != "preset" {
		t.Errorf("unexpected GET response data: %+v", getResp.Data)
	}
}

func TestPutTheme_CustomCSSAndScoping(t *testing.T) {
	_, handler, _, user := newTestEnv(t)
	cur := authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
	r := newTestRouter(handler, &cur)

	rawCSS := `.header { font-size: 20px; } a:hover { color: blue; }`
	payload := `{
		"is_enabled": true,
		"mode": "custom_css",
		"preset_config": {},
		"custom_css": "` + rawCSS + `"
	}`
	w := themeRequest(t, r, http.MethodPut, "/api/v1/users/me/theme", payload)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data UserThemeDTO `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	expectedScope := `.creator-theme-scope[data-creator-id="` + user.UUID.String() + `"]`
	if !strings.Contains(resp.Data.SanitizedCSS, expectedScope) {
		t.Fatalf("expected sanitized CSS to contain %q, got: %s", expectedScope, resp.Data.SanitizedCSS)
	}
	if resp.Data.CustomCSS != rawCSS {
		t.Fatalf("expected custom_css to match original raw CSS, got %q", resp.Data.CustomCSS)
	}
}

func TestPutTheme_InvalidCSS_TooLarge(t *testing.T) {
	_, handler, _, user := newTestEnv(t)
	cur := authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
	r := newTestRouter(handler, &cur)

	oversizedCSS := strings.Repeat("body { color: red; }\n", 4000) // > 64KB
	payloadMap := map[string]interface{}{
		"is_enabled": true,
		"mode":       "custom_css",
		"custom_css": oversizedCSS,
	}
	body, _ := json.Marshal(payloadMap)

	w := themeRequest(t, r, http.MethodPut, "/api/v1/users/me/theme", string(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"theme.invalid_css"`) {
		t.Fatalf("expected theme.invalid_css error code, got: %s", w.Body.String())
	}
}

func TestPutTheme_InvalidMode(t *testing.T) {
	_, handler, _, user := newTestEnv(t)
	cur := authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
	r := newTestRouter(handler, &cur)

	w := themeRequest(t, r, http.MethodPut, "/api/v1/users/me/theme", `{"mode":"unsupported_mode"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"theme.invalid_mode"`) {
		t.Fatalf("expected theme.invalid_mode error code, got: %s", w.Body.String())
	}
}

func TestDeleteTheme_ResetTheme(t *testing.T) {
	_, handler, _, user := newTestEnv(t)
	cur := authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
	r := newTestRouter(handler, &cur)

	// 1. Create a theme
	putPayload := `{"is_enabled":true,"mode":"preset","preset_config":{"accent":"#123456"}}`
	wPut := themeRequest(t, r, http.MethodPut, "/api/v1/users/me/theme", putPayload)
	if wPut.Code != http.StatusOK {
		t.Fatalf("expected 200 on PUT, got %d: %s", wPut.Code, wPut.Body.String())
	}

	// 2. Delete theme
	wDel := themeRequest(t, r, http.MethodDelete, "/api/v1/users/me/theme", "")
	if wDel.Code != http.StatusOK {
		t.Fatalf("expected 200 on DELETE, got %d: %s", wDel.Code, wDel.Body.String())
	}

	// 3. Verify public GET returns default theme
	wGet := themeRequest(t, r, http.MethodGet, "/api/v1/users/alice/theme", "")
	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200 on GET, got %d: %s", wGet.Code, wGet.Body.String())
	}
	var getResp struct {
		Data UserThemeDTO `json:"data"`
	}
	if err := json.Unmarshal(wGet.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if getResp.Data.IsEnabled {
		t.Errorf("expected is_enabled to be false after delete, got true")
	}

	// 4. Verify putting a new theme after delete works without uniqueness error
	putPayload2 := `{"is_enabled":true,"mode":"preset","preset_config":{"accent":"#654321"}}`
	wPut2 := themeRequest(t, r, http.MethodPut, "/api/v1/users/me/theme", putPayload2)
	if wPut2.Code != http.StatusOK {
		t.Fatalf("expected 200 on PUT after delete, got %d: %s", wPut2.Code, wPut2.Body.String())
	}
}
