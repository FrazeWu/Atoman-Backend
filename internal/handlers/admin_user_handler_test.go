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

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type adminUserTestEnv struct {
	db     *gorm.DB
	router *gin.Engine
	token  string
}

func newAdminUserTestEnv(t *testing.T, role string) adminUserTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := newAuthTestDB(t)
	actor := model.User{
		Username: "management-actor",
		Email:    "management-actor@example.com",
		Password: "hash",
		Role:     role,
		IsActive: true,
	}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatalf("create actor: %v", err)
	}
	credentials, err := authsession.New(db).Create(actor.UUID, authsession.KindAPI)
	if err != nil {
		t.Fatalf("create actor session: %v", err)
	}
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	router := gin.New()
	SetupAdminUserRoutes(router, db)
	return adminUserTestEnv{db: db, router: router, token: credentials.Token}
}

func (env adminUserTestEnv) request(t *testing.T, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+env.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	env.router.ServeHTTP(response, req)
	return response
}

func seedManagedUser(t *testing.T, db *gorm.DB, username, role string, active bool) model.User {
	t.Helper()
	user := model.User{
		Username:    username,
		Email:       username + "@example.com",
		Password:    "hash",
		Role:        role,
		DisplayName: username + " display",
		IsActive:    active,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create managed user: %v", err)
	}
	if !active {
		if err := db.Model(&user).Update("is_active", false).Error; err != nil {
			t.Fatalf("deactivate managed user: %v", err)
		}
		user.IsActive = false
	}
	return user
}

func decodeAdminUserResponse(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v: %s", err, response.Body.String())
	}
	return payload
}

func TestAdminUserListSupportsSearchStatusAndPagination(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleAdmin)
	seedManagedUser(t, env.db, "active-member", authctx.RoleUser, true)
	inactive := seedManagedUser(t, env.db, "inactive-member", authctx.RoleUser, false)
	deleted := seedManagedUser(t, env.db, "deleted-member", authctx.RoleUser, true)
	if err := env.db.Delete(&deleted).Error; err != nil {
		t.Fatalf("soft delete fixture: %v", err)
	}

	response := env.request(t, http.MethodGet, "/api/v1/admin/users?q=inactive&status=inactive&page=1&page_size=10", "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	payload := decodeAdminUserResponse(t, response)
	data := payload["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["uuid"] != inactive.UUID.String() {
		t.Fatalf("unexpected users: %#v", data)
	}
	meta := payload["meta"].(map[string]any)
	if meta["page"] != float64(1) || meta["page_size"] != float64(10) || meta["total"] != float64(1) {
		t.Fatalf("unexpected pagination: %#v", meta)
	}
}

func TestAdminCanCreateOnlyRegularUsers(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleAdmin)
	response := env.request(t, http.MethodPost, "/api/v1/admin/users", `{"username":"new-member","email":"NEW@example.com","display_name":"New Member","password":"secret123","role":"user"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
	var user model.User
	if err := env.db.First(&user, "username = ?", "new-member").Error; err != nil {
		t.Fatalf("load created user: %v", err)
	}
	if user.Email != "new@example.com" || user.DisplayName != "New Member" || !user.IsActive {
		t.Fatalf("unexpected created user: %#v", user)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte("secret123")); err != nil {
		t.Fatalf("password was not hashed: %v", err)
	}
	if err := env.db.First(&model.UserSettings{}, "user_id = ?", user.UUID).Error; err != nil {
		t.Fatalf("load user settings: %v", err)
	}
	var channels int64
	if err := env.db.Model(&model.Channel{}).Where("user_id = ?", user.UUID).Count(&channels).Error; err != nil {
		t.Fatalf("count default channels: %v", err)
	}
	if channels != 1 {
		t.Fatalf("expected one default channel, got %d", channels)
	}
	var groups int64
	if err := env.db.Model(&model.SubscriptionGroup{}).Where("user_id = ?", user.UUID).Count(&groups).Error; err != nil {
		t.Fatalf("count subscription groups: %v", err)
	}
	if groups != 1 {
		t.Fatalf("expected one default subscription group, got %d", groups)
	}
	var folders int64
	if err := env.db.Model(&model.BookmarkFolder{}).Where("user_id = ?", user.UUID).Count(&folders).Error; err != nil {
		t.Fatalf("count bookmark folders: %v", err)
	}
	if folders != 1 {
		t.Fatalf("expected one default bookmark folder, got %d", folders)
	}
	var playlists int64
	if err := env.db.Model(&model.Playlist{}).Where("user_id = ?", user.UUID).Count(&playlists).Error; err != nil {
		t.Fatalf("count music playlists: %v", err)
	}
	if playlists != 0 {
		t.Fatalf("expected no implicit favorite playlist, got %d", playlists)
	}

	forbidden := env.request(t, http.MethodPost, "/api/v1/admin/users", `{"username":"new-admin","email":"new-admin@example.com","password":"secret123","role":"admin"}`)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", forbidden.Code, forbidden.Body.String())
	}
}

func TestOwnerCanCreateAndEditAdministrators(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleOwner)
	created := env.request(t, http.MethodPost, "/api/v1/admin/users", `{"username":"managed-admin","email":"managed-admin@example.com","password":"secret123","role":"admin"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", created.Code, created.Body.String())
	}
	createdPayload := decodeAdminUserResponse(t, created)
	userID := createdPayload["data"].(map[string]any)["uuid"].(string)

	updated := env.request(t, http.MethodPatch, "/api/v1/admin/users/"+userID, `{"username":"renamed-admin","email":"renamed@example.com","display_name":"Renamed","role":"user"}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", updated.Code, updated.Body.String())
	}
	var user model.User
	if err := env.db.First(&user, "uuid = ?", userID).Error; err != nil {
		t.Fatalf("load updated user: %v", err)
	}
	if user.Username != "renamed-admin" || user.Email != "renamed@example.com" || user.DisplayName != "Renamed" || user.Role != authctx.RoleUser {
		t.Fatalf("unexpected updated user: %#v", user)
	}
}

func TestAdminCannotManageAdministratorsOrChangeRoles(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleAdmin)
	admin := seedManagedUser(t, env.db, "other-admin", authctx.RoleAdmin, true)
	member := seedManagedUser(t, env.db, "role-member", authctx.RoleUser, true)

	response := env.request(t, http.MethodPatch, "/api/v1/admin/users/"+admin.UUID.String(), `{"display_name":"Changed"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected admin target to be forbidden, got %d: %s", response.Code, response.Body.String())
	}
	response = env.request(t, http.MethodPatch, "/api/v1/admin/users/"+member.UUID.String(), `{"role":"admin"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected role change to be forbidden, got %d: %s", response.Code, response.Body.String())
	}
}

func TestAdminCanDeactivateRestoreAndRevokeSessions(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleAdmin)
	member := seedManagedUser(t, env.db, "status-member", authctx.RoleUser, true)
	sessions := authsession.New(env.db)
	credentials, err := sessions.Create(member.UUID, authsession.KindAPI)
	if err != nil {
		t.Fatalf("create member session: %v", err)
	}

	response := env.request(t, http.MethodPut, "/api/v1/admin/users/"+member.UUID.String()+"/status", `{"is_active":false}`)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if _, err := sessions.Authenticate(credentials.Token, authsession.KindAPI); !errors.Is(err, authsession.ErrInvalid) {
		t.Fatalf("expected member session to be revoked, got %v", err)
	}
	if err := env.db.First(&member, "uuid = ?", member.UUID).Error; err != nil || member.IsActive {
		t.Fatalf("expected inactive member, user %#v, err %v", member, err)
	}
	if member.AuthVersion != 1 {
		t.Fatalf("expected deactivation to advance auth version, got %d", member.AuthVersion)
	}

	restored := env.request(t, http.MethodPut, "/api/v1/admin/users/"+member.UUID.String()+"/status", `{"is_active":true}`)
	if restored.Code != http.StatusOK {
		t.Fatalf("expected restore 200, got %d: %s", restored.Code, restored.Body.String())
	}
	if err := env.db.First(&member, "uuid = ?", member.UUID).Error; err != nil || !member.IsActive {
		t.Fatalf("expected active member, user %#v, err %v", member, err)
	}
	stale, err := sessions.CreateAtVersion(member.UUID, authsession.KindAPI, 0)
	if err != nil {
		t.Fatalf("create stale session: %v", err)
	}
	if _, err := sessions.Authenticate(stale.Token, authsession.KindAPI); !errors.Is(err, authsession.ErrInvalid) {
		t.Fatalf("expected stale session to remain invalid after restore, got %v", err)
	}
}

func TestAdminCanResetPasswordAndRevokeSessions(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleAdmin)
	member := seedManagedUser(t, env.db, "password-member", authctx.RoleUser, true)
	sessions := authsession.New(env.db)
	credentials, err := sessions.Create(member.UUID, authsession.KindAPI)
	if err != nil {
		t.Fatalf("create member session: %v", err)
	}

	response := env.request(t, http.MethodPut, "/api/v1/admin/users/"+member.UUID.String()+"/password", `{"password":"replacement"}`)
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", response.Code, response.Body.String())
	}
	if _, err := sessions.Authenticate(credentials.Token, authsession.KindAPI); !errors.Is(err, authsession.ErrInvalid) {
		t.Fatalf("expected member session to be revoked, got %v", err)
	}
	if err := env.db.First(&member, "uuid = ?", member.UUID).Error; err != nil {
		t.Fatalf("load member: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(member.Password), []byte("replacement")); err != nil {
		t.Fatalf("password was not replaced: %v", err)
	}
}

func TestAdminCanSoftDeleteUserAndRevokeSessions(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleAdmin)
	member := seedManagedUser(t, env.db, "deleted-account", authctx.RoleUser, true)
	sessions := authsession.New(env.db)
	credentials, err := sessions.Create(member.UUID, authsession.KindAPI)
	if err != nil {
		t.Fatalf("create member session: %v", err)
	}

	response := env.request(t, http.MethodDelete, "/api/v1/admin/users/"+member.UUID.String(), "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", response.Code, response.Body.String())
	}
	if _, err := sessions.Authenticate(credentials.Token, authsession.KindAPI); !errors.Is(err, authsession.ErrInvalid) {
		t.Fatalf("expected member session to be revoked, got %v", err)
	}
	if err := env.db.First(&model.User{}, "uuid = ?", member.UUID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected soft-deleted user to be hidden, got %v", err)
	}
	var deleted model.User
	if err := env.db.Unscoped().First(&deleted, "uuid = ?", member.UUID).Error; err != nil || !deleted.DeletedAt.Valid {
		t.Fatalf("expected deleted_at to be set, user %#v, err %v", deleted, err)
	}
	if deleted.AuthVersion != 1 {
		t.Fatalf("expected deletion to advance auth version, got %d", deleted.AuthVersion)
	}
}

func TestAdminUserMutationsRejectInvalidUUID(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleOwner)
	response := env.request(t, http.MethodDelete, "/api/v1/admin/users/not-a-uuid", "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
	payload := decodeAdminUserResponse(t, response)
	errorPayload := payload["error"].(map[string]any)
	if errorPayload["code"] != "admin_user.invalid_id" {
		t.Fatalf("unexpected error: %#v", errorPayload)
	}
}

func TestOwnerCannotDeleteOwnerAccount(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleOwner)
	owner := seedManagedUser(t, env.db, "other-owner", authctx.RoleOwner, true)
	response := env.request(t, http.MethodDelete, "/api/v1/admin/users/"+owner.UUID.String(), "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", response.Code, response.Body.String())
	}
}

func TestAdminUserRouteRequiresAdmin(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleUser)
	response := env.request(t, http.MethodGet, "/api/v1/admin/users", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", response.Code, response.Body.String())
	}
}

func TestAdminUserPasswordEndpointRejectsShortPassword(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleAdmin)
	member := seedManagedUser(t, env.db, "short-password", authctx.RoleUser, true)
	response := env.request(t, http.MethodPut, "/api/v1/admin/users/"+member.UUID.String()+"/password", `{"password":"123"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
}

func TestAdminCanInspectLoginHistoryAndRevokeSessionWithAudit(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleAdmin)
	member := seedManagedUser(t, env.db, "security-member", authctx.RoleUser, true)
	credentials, err := authsession.New(env.db).Create(member.UUID, authsession.KindWeb, authsession.Metadata{
		UserAgent: "Mozilla/5.0 (Macintosh) Safari/605.1.15",
		IPAddress: "203.0.113.19",
		IPPrefix:  "203.0.113.0/24",
	})
	if err != nil {
		t.Fatalf("create member session: %v", err)
	}
	event := model.LoginEvent{
		UserID: member.UUID, SessionID: &credentials.SessionID, Method: "password", Result: model.LoginResultSucceeded,
		IPAddress: "203.0.113.19", IPPrefix: "203.0.113.0/24", CountryCode: "DE", City: "Berlin",
		UserAgent: "Mozilla/5.0 (Macintosh) Safari/605.1.15", CreatedAt: time.Now().UTC(),
	}
	if err := env.db.Create(&event).Error; err != nil {
		t.Fatalf("create login event: %v", err)
	}

	detail := env.request(t, http.MethodGet, "/api/v1/admin/users/"+member.UUID.String(), "")
	if detail.Code != http.StatusOK {
		t.Fatalf("expected detail 200, got %d: %s", detail.Code, detail.Body.String())
	}
	detailData := decodeAdminUserResponse(t, detail)["data"].(map[string]any)
	if detailData["last_login_ip"] != "203.0.113.19" || detailData["last_login_location"] != "Berlin · DE" || detailData["active_sessions"] != float64(1) {
		t.Fatalf("unexpected detail summary: %#v", detailData)
	}

	history := env.request(t, http.MethodGet, "/api/v1/admin/users/"+member.UUID.String()+"/login-events", "")
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), `"device_name":"Mac 浏览器"`) {
		t.Fatalf("unexpected login history: %d %s", history.Code, history.Body.String())
	}

	revoked := env.request(t, http.MethodDelete, "/api/v1/admin/users/"+member.UUID.String()+"/sessions/"+credentials.SessionID.String(), "")
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("expected revoke 204, got %d: %s", revoked.Code, revoked.Body.String())
	}
	var auditLog model.AuditLog
	if err := env.db.Last(&auditLog, "entity_id = ? AND action = ?", member.UUID, "admin_user.session_revoked").Error; err != nil {
		t.Fatalf("load session audit: %v", err)
	}
	if !strings.Contains(auditLog.Metadata, "security-member") || !strings.Contains(auditLog.Metadata, credentials.SessionID.String()) {
		t.Fatalf("unexpected audit metadata: %s", auditLog.Metadata)
	}
}

func TestAdminUserMutationsWriteAuditLogs(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleAdmin)
	member := seedManagedUser(t, env.db, "audited-member", authctx.RoleUser, true)
	response := env.request(t, http.MethodPut, "/api/v1/admin/users/"+member.UUID.String()+"/status", `{"is_active":false}`)
	if response.Code != http.StatusOK {
		t.Fatalf("expected status update 200, got %d: %s", response.Code, response.Body.String())
	}
	var count int64
	if err := env.db.Model(&model.AuditLog{}).Where("entity_id = ? AND action = ?", member.UUID, "admin_user.deactivated").Count(&count).Error; err != nil {
		t.Fatalf("count audit logs: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one deactivation audit, got %d", count)
	}
}

func TestAdminUserCreateRejectsDuplicateEmail(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleOwner)
	seedManagedUser(t, env.db, "existing-member", authctx.RoleUser, true)
	response := env.request(t, http.MethodPost, "/api/v1/admin/users", `{"username":"different-member","email":"EXISTING-MEMBER@example.com","password":"secret123","role":"user"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", response.Code, response.Body.String())
	}
}

func TestAdminUserUpdateRejectsUnknownUser(t *testing.T) {
	env := newAdminUserTestEnv(t, authctx.RoleOwner)
	response := env.request(t, http.MethodPatch, "/api/v1/admin/users/"+uuid.NewString(), `{"display_name":"Missing"}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", response.Code, response.Body.String())
	}
}
