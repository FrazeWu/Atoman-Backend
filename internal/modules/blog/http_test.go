package blog

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func newBlogHTTPTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
		&model.BlogPostRating{},
		&model.AuditLog{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
	)

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: authctx.RoleUser, DisplayName: "Alice", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	return NewService(db), db, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser}
}

func newBlogHTTPRouter(service *Service, current *authctx.CurrentUser) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if current != nil {
			authctx.SetCurrentUser(c, *current)
		}
		c.Next()
	})
	v1 := r.Group("/api/v1")
	RegisterRoutes(v1.Group("/blog"), service)
	return r
}

func TestRegisterRoutesCreatePostRequiresCurrentUser(t *testing.T) {
	service, _, _ := newBlogHTTPTestService(t)
	r := newBlogHTTPRouter(service, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts", bytes.NewBufferString(`{"title":"hello"}`))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterRoutesCreatePostRejectsInvalidJSON(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	r := newBlogHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts", bytes.NewBufferString(`{"title":`))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "validation.invalid_request") {
		t.Fatalf("expected validation.invalid_request, got %s", w.Body.String())
	}
}

func TestRegisterRoutesSetRatingRejectsInvalidUUID(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	r := newBlogHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/not-a-uuid/rating", bytes.NewBufferString(`{"score":8}`))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "validation.invalid_request") {
		t.Fatalf("expected validation.invalid_request, got %s", w.Body.String())
	}
}

func TestRegisterRoutesSetRatingForbidsAuthorSelfRating(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	post, err := service.CreatePost(user, CreatePostRequest{Title: "Hello", Content: "world", ChannelID: channel.ID, Status: "published"})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String()+"/rating", bytes.NewBufferString(`{"score":8}`))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "blog.rating_self_forbidden") {
		t.Fatalf("expected blog.rating_self_forbidden, got %s", w.Body.String())
	}
}

func TestRegisterRoutesCreatePostReturnsCreatedPost(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	defaultCollectionName := ensureDefaultCollectionName()
	var collection model.Collection
	if err := service.db.Where("channel_id = ? AND name = ?", channel.ID, defaultCollectionName).First(&collection).Error; err != nil {
		t.Fatalf("load default collection: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	body := map[string]any{
		"title":          "HTTP Post",
		"content":        "content",
		"excerpt":        "summary",
		"cover_url":      "https://example.com/cover.png",
		"channel_id":     channel.ID,
		"collection_ids": []string{collection.ID.String()},
		"visibility":     "public",
		"status":         "draft",
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data model.Post `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.ID.String() == "00000000-0000-0000-0000-000000000000" || resp.Data.Title != "HTTP Post" || resp.Data.UserID != user.ID {
		t.Fatalf("unexpected created post: %#v", resp.Data)
	}
	if resp.Data.ChannelID == nil || *resp.Data.ChannelID != channel.ID {
		t.Fatalf("expected channel id %s, got %#v", channel.ID, resp.Data.ChannelID)
	}
}
