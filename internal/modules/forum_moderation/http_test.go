package forum_moderation

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func newForumModerationHTTPRouter(service *Service, current *authctx.CurrentUser) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if current != nil {
			authctx.SetCurrentUser(c, *current)
		}
		c.Next()
	})
	v1 := r.Group("/api/v1/forum/moderation")
	RegisterRoutes(v1, service)
	return r
}

func seedForumModerationHTTPUsers(t *testing.T) (*Service, authctx.CurrentUser, authctx.CurrentUser, model.ForumCategory, model.User) {
	t.Helper()

	service, db, admin := newForumModerationTestService(t)

	category := model.ForumCategory{Name: "General", Description: "general", Color: "#111111"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}

	moderator := model.User{
		Username: "mod-http",
		Email:    "mod-http@example.com",
		Password: "hash",
		Role:     authctx.RoleModerator,
		IsActive: true,
	}
	if err := db.Create(&moderator).Error; err != nil {
		t.Fatalf("create moderator: %v", err)
	}

	user := model.User{
		Username: "user-http",
		Email:    "user-http@example.com",
		Password: "hash",
		Role:     authctx.RoleUser,
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	return service, admin, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}, category, moderator
}

func TestModeratorAssignmentRoutesRejectNonAdminUser(t *testing.T) {
	service, _, normalUser, _, _ := seedForumModerationHTTPUsers(t)
	router := newForumModerationHTTPRouter(service, &normalUser)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/forum/moderation/moderators", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin list moderators, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestUserModerationActionRoutes(t *testing.T) {
	service, admin, normalUser, _, _ := seedForumModerationHTTPUsers(t)
	router := newForumModerationHTTPRouter(service, &admin)

	body := bytes.NewBufferString(`{"action":"silence","reason":"刷屏","duration_hours":24}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/forum/moderation/users/"+normalUser.ID.String()+"/actions", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/forum/moderation/user-actions?user_id="+normalUser.ID.String()+"&page=1&page_size=1", nil)
	listRR := httptest.NewRecorder()
	router.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", listRR.Code, listRR.Body.String())
	}
	var payload struct {
		Data []model.ForumUserModerationAction `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Meta.Total != 1 {
		t.Fatalf("unexpected payload: %s", listRR.Body.String())
	}
}

func TestModerationUsersRouteIncludesInactiveAndRejectsNonAdmin(t *testing.T) {
	service, db, admin := newForumModerationTestService(t)
	banned := model.User{Username: "banned-http", Email: "banned-http@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&banned).Error; err != nil {
		t.Fatal(err)
	}
	router := newForumModerationHTTPRouter(service, &admin)
	ban := httptest.NewRecorder()
	banBody := bytes.NewBufferString(`{"action":"ban","reason":"违规"}`)
	banReq := httptest.NewRequest(http.MethodPost, "/api/v1/forum/moderation/users/"+banned.UUID.String()+"/actions", banBody)
	banReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(ban, banReq)
	if ban.Code != http.StatusCreated {
		t.Fatalf("expected ban 201, got %d: %s", ban.Code, ban.Body.String())
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/forum/moderation/users?q=banned&page_size=20", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var payload struct {
		Data []ModerationUser `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0].UUID != banned.UUID || payload.Data[0].IsActive {
		t.Fatalf("unexpected payload: %s", rr.Body.String())
	}
	unban := httptest.NewRecorder()
	unbanBody := bytes.NewBufferString(`{"action":"unban"}`)
	unbanReq := httptest.NewRequest(http.MethodPost, "/api/v1/forum/moderation/users/"+banned.UUID.String()+"/actions", unbanBody)
	unbanReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(unban, unbanReq)
	if unban.Code != http.StatusCreated {
		t.Fatalf("expected unban 201, got %d: %s", unban.Code, unban.Body.String())
	}
	var restored model.User
	if err := db.First(&restored, "uuid = ?", banned.UUID).Error; err != nil || !restored.IsActive {
		t.Fatalf("expected active after unban: active=%v err=%v", restored.IsActive, err)
	}

	normal := authctx.CurrentUser{ID: banned.UUID, Username: banned.Username, Role: authctx.RoleUser}
	denied := httptest.NewRecorder()
	newForumModerationHTTPRouter(service, &normal).ServeHTTP(denied, httptest.NewRequest(http.MethodGet, "/api/v1/forum/moderation/users", nil))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", denied.Code)
	}
}

func TestModeratorAssignmentRoutesAllowAdminCRUD(t *testing.T) {
	service, admin, _, category, moderator := seedForumModerationHTTPUsers(t)
	router := newForumModerationHTTPRouter(service, &admin)

	createBody := bytes.NewBufferString(`{
	  "user_id": "` + moderator.UUID.String() + `",
	  "category_id": "` + category.ID.String() + `",
	  "can_review_category_request": true,
	  "can_pin_topic": true,
	  "can_lock_topic": false
	}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/forum/moderation/moderators", createBody)
	createReq.Header.Set("Content-Type", "application/json")
	createRR := httptest.NewRecorder()
	router.ServeHTTP(createRR, createReq)

	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected create 201, got %d: %s", createRR.Code, createRR.Body.String())
	}

	var created struct {
		Data model.ForumModeratorAssignment `json:"data"`
	}
	if err := json.Unmarshal(createRR.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Data.ID == uuid.Nil {
		t.Fatalf("expected created assignment id, got %s", createRR.Body.String())
	}
	if !created.Data.CanPinTopic || created.Data.CanLockTopic {
		t.Fatalf("unexpected created permissions: %#v", created.Data)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/forum/moderation/moderators", nil)
	listRR := httptest.NewRecorder()
	router.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", listRR.Code, listRR.Body.String())
	}

	updateBody := bytes.NewBufferString(`{
	  "user_id": "` + moderator.UUID.String() + `",
	  "category_id": "` + category.ID.String() + `",
	  "can_review_category_request": false,
	  "can_pin_topic": false,
	  "can_lock_topic": true
	}`)
	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/forum/moderation/moderators/"+created.Data.ID.String(), updateBody)
	updateReq.Header.Set("Content-Type", "application/json")
	updateRR := httptest.NewRecorder()
	router.ServeHTTP(updateRR, updateReq)
	if updateRR.Code != http.StatusOK {
		t.Fatalf("expected update 200, got %d: %s", updateRR.Code, updateRR.Body.String())
	}

	var updated struct {
		Data model.ForumModeratorAssignment `json:"data"`
	}
	if err := json.Unmarshal(updateRR.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated.Data.CanPinTopic || !updated.Data.CanLockTopic {
		t.Fatalf("unexpected updated permissions: %#v", updated.Data)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/forum/moderation/moderators/"+created.Data.ID.String(), nil)
	deleteRR := httptest.NewRecorder()
	router.ServeHTTP(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusNoContent {
		t.Fatalf("expected delete 204, got %d: %s", deleteRR.Code, deleteRR.Body.String())
	}

	finalListReq := httptest.NewRequest(http.MethodGet, "/api/v1/forum/moderation/moderators", nil)
	finalListRR := httptest.NewRecorder()
	router.ServeHTTP(finalListRR, finalListReq)
	if finalListRR.Code != http.StatusOK {
		t.Fatalf("expected final list 200, got %d: %s", finalListRR.Code, finalListRR.Body.String())
	}

	var finalList struct {
		Data []model.ForumModeratorAssignment `json:"data"`
	}
	if err := json.Unmarshal(finalListRR.Body.Bytes(), &finalList); err != nil {
		t.Fatalf("decode final list response: %v", err)
	}
	if len(finalList.Data) != 0 {
		t.Fatalf("expected no assignments after delete, got %#v", finalList.Data)
	}
}
