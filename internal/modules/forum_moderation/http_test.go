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
	"github.com/stretchr/testify/require"
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

func TestReplyReportMapsTrimmedCoreErrorsWithout500(t *testing.T) {
	ctx := newForumModerationCommentContext(t)
	current := ctx.participant
	router := newForumModerationHTTPRouter(ctx.service, &current)

	request := func(targetID string) *httptest.ResponseRecorder {
		body := bytes.NewBufferString(`{"target_type":" reply ","target_id":"` + targetID + `","reason":"spam","note":""}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/forum/moderation/report", body)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	require.Equal(t, http.StatusNotFound, request(uuid.NewString()).Code)
	created := ctx.createReply(t, ctx.participant, "own reply")
	require.Equal(t, http.StatusForbidden, request(created.ID.String()).Code)
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
