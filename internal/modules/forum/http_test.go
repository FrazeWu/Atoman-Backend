package forum

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type forumHTTPResponse struct {
	Data json.RawMessage `json:"data"`
	Meta struct {
		Page     int   `json:"page"`
		PageSize int   `json:"page_size"`
		Total    int64 `json:"total"`
	} `json:"meta"`
}

func newForumHTTPTestRouter(t *testing.T) (*gin.Engine, *gorm.DB, model.User, model.ForumCategory) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumReply{},
		&model.ForumDraft{},
	)
	user := model.User{Username: "forum-owner", Email: "forum-owner@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	category := model.ForumCategory{Name: "General", Description: "General topics", Color: "#111111"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role})
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1/forum"), NewService(db))
	return router, db, user, category
}

func performForumRequest(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload *bytes.Reader
	if body == nil {
		payload = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		payload = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, payload)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func decodeForumData[T any](t *testing.T, response *httptest.ResponseRecorder) (T, forumHTTPResponse) {
	t.Helper()
	var envelope forumHTTPResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response envelope: %v: %s", err, response.Body.String())
	}
	var data T
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode response data: %v: %s", err, response.Body.String())
	}
	return data, envelope
}

func TestNestedReplyRouteUsesTopicIDFromPath(t *testing.T) {
	router, db, user, category := newForumHTTPTestRouter(t)
	topic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "Topic", Content: "Body"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}

	response := performForumRequest(t, router, http.MethodPost, "/api/v1/forum/topics/"+topic.ID.String()+"/replies", map[string]any{"content": "Nested reply"})
	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
	reply, _ := decodeForumData[model.ForumReply](t, response)
	if reply.TopicID != topic.ID || reply.Content != "Nested reply" {
		t.Fatalf("unexpected reply: %#v", reply)
	}
}

func TestCreateAndUpdateTopicPersistNormalizedTags(t *testing.T) {
	router, _, _, category := newForumHTTPTestRouter(t)
	response := performForumRequest(t, router, http.MethodPost, "/api/v1/forum/topics", map[string]any{
		"category_id": category.ID,
		"title":       "Tagged topic",
		"content":     "Body",
		"tags":        []string{" Go ", "", "Go", "Vue"},
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
	topic, _ := decodeForumData[model.ForumTopic](t, response)
	if got := []string(topic.Tags); len(got) != 2 || got[0] != "Go" || got[1] != "Vue" {
		t.Fatalf("expected normalized tags, got %#v", got)
	}

	response = performForumRequest(t, router, http.MethodPut, "/api/v1/forum/topics/"+topic.ID.String(), map[string]any{
		"title": "Tagged topic", "content": "Body", "tags": []string{" Rust ", "Rust", "SQLite"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	updated, _ := decodeForumData[model.ForumTopic](t, response)
	if got := []string(updated.Tags); len(got) != 2 || got[0] != "Rust" || got[1] != "SQLite" {
		t.Fatalf("expected updated normalized tags, got %#v", got)
	}
}

func TestCreateTopicRejectsInvalidTags(t *testing.T) {
	router, _, _, category := newForumHTTPTestRouter(t)
	tests := []struct {
		name string
		tags []string
	}{
		{name: "more than five", tags: []string{"1", "2", "3", "4", "5", "6"}},
		{name: "longer than thirty characters", tags: []string{"1234567890123456789012345678901"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performForumRequest(t, router, http.MethodPost, "/api/v1/forum/topics", map[string]any{
				"category_id": category.ID, "title": "Topic", "content": "Body", "tags": test.tags,
			})
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestListTopicsSupportsFiltersSortAndPagination(t *testing.T) {
	router, db, user, category := newForumHTTPTestRouter(t)
	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	activeAt := base.Add(4 * time.Hour)
	topics := []model.ForumTopic{
		{Base: model.Base{CreatedAt: base}, UserID: user.UUID, CategoryID: category.ID, Title: "Go first", Content: "alpha", Tags: model.StringSlice{"go"}, LikeCount: 1},
		{Base: model.Base{CreatedAt: base.Add(time.Hour)}, UserID: user.UUID, CategoryID: category.ID, Title: "Go top", Content: "beta", Tags: model.StringSlice{"go", "db"}, LikeCount: 9, ReplyCount: 2},
		{Base: model.Base{CreatedAt: base.Add(2 * time.Hour)}, UserID: user.UUID, CategoryID: category.ID, Title: "Other", Content: "gamma", Tags: model.StringSlice{"rust"}, Featured: true, LastReplyAt: &activeAt},
	}
	for index := range topics {
		if err := db.Create(&topics[index]).Error; err != nil {
			t.Fatalf("create topic %d: %v", index, err)
		}
	}

	response := performForumRequest(t, router, http.MethodGet, "/api/v1/forum/topics?sort=top&tag=go&search=Go&page=1&page_size=1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	listed, envelope := decodeForumData[[]model.ForumTopic](t, response)
	if envelope.Meta.Total != 2 || envelope.Meta.PageSize != 1 || len(listed) != 1 || listed[0].Title != "Go top" {
		t.Fatalf("unexpected filtered page: data=%#v meta=%#v", listed, envelope.Meta)
	}

	response = performForumRequest(t, router, http.MethodGet, "/api/v1/forum/topics?sort=active&limit=2", nil)
	listed, envelope = decodeForumData[[]model.ForumTopic](t, response)
	if envelope.Meta.PageSize != 2 || len(listed) != 2 || listed[0].Title != "Other" {
		t.Fatalf("unexpected active/limit result: data=%#v meta=%#v", listed, envelope.Meta)
	}

	response = performForumRequest(t, router, http.MethodGet, "/api/v1/forum/topics?sort=featured", nil)
	listed, _ = decodeForumData[[]model.ForumTopic](t, response)
	if len(listed) != 3 || listed[0].Title != "Other" {
		t.Fatalf("unexpected featured result: %#v", listed)
	}
}

func TestListRepliesSupportsBestSort(t *testing.T) {
	router, db, user, category := newForumHTTPTestRouter(t)
	topic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "Topic", Content: "Body"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}
	replies := []model.ForumReply{
		{TopicID: topic.ID, UserID: user.UUID, Content: "first", FloorNumber: 1, LikeCount: 2},
		{TopicID: topic.ID, UserID: user.UUID, Content: "best", FloorNumber: 2, LikeCount: 8},
		{TopicID: topic.ID, UserID: user.UUID, Content: "tie", FloorNumber: 3, LikeCount: 8},
	}
	for index := range replies {
		if err := db.Create(&replies[index]).Error; err != nil {
			t.Fatalf("create reply %d: %v", index, err)
		}
	}
	response := performForumRequest(t, router, http.MethodGet, "/api/v1/forum/topics/"+topic.ID.String()+"/replies?sort=best", nil)
	listed, _ := decodeForumData[[]model.ForumReply](t, response)
	if len(listed) != 3 || listed[0].FloorNumber != 2 || listed[1].FloorNumber != 3 || listed[2].FloorNumber != 1 {
		t.Fatalf("unexpected best order: %#v", listed)
	}
}

func TestDraftContextKeyReturnsAndDeletesSingleUserDraft(t *testing.T) {
	router, db, user, _ := newForumHTTPTestRouter(t)
	drafts := []model.ForumDraft{
		{UserID: user.UUID, ContextKey: "new_topic", Title: "new topic"},
		{UserID: user.UUID, ContextKey: "reply:topic-1", Content: "reply body"},
	}
	for index := range drafts {
		if err := db.Create(&drafts[index]).Error; err != nil {
			t.Fatalf("create draft %d: %v", index, err)
		}
	}

	response := performForumRequest(t, router, http.MethodGet, "/api/v1/forum/drafts?context_key=reply%3Atopic-1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	draft, _ := decodeForumData[*model.ForumDraft](t, response)
	if draft == nil || draft.ContextKey != "reply:topic-1" {
		t.Fatalf("expected matching draft, got %#v", draft)
	}

	response = performForumRequest(t, router, http.MethodGet, "/api/v1/forum/drafts?context_key=missing", nil)
	missing, _ := decodeForumData[*model.ForumDraft](t, response)
	if missing != nil {
		t.Fatalf("expected null draft, got %#v", missing)
	}

	response = performForumRequest(t, router, http.MethodDelete, "/api/v1/forum/drafts?context_key=reply%3Atopic-1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var count int64
	if err := db.Model(&model.ForumDraft{}).Where("user_id = ? AND context_key = ?", user.UUID, "reply:topic-1").Count(&count).Error; err != nil {
		t.Fatalf("count draft: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected context draft deleted, count=%d", count)
	}
}
