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
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
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
		&model.PostCollection{},
		&model.BlogPostRating{},
		&model.BlogDraft{},
		&model.Comment{},
		&model.Like{},
		&model.Bookmark{},
		&model.BookmarkFolder{},
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

func TestRegisterRoutesMountsChannelReadEndpointsAndEnsureDefault(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	secondary := model.Collection{ChannelID: channel.ID, Name: "Featured", Description: "featured"}
	if err := db.Create(&secondary).Error; err != nil {
		t.Fatalf("create secondary collection: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)

	for _, path := range []string{
		"/api/v1/blog/channels",
		"/api/v1/blog/channels?user_id=" + user.ID.String(),
		"/api/v1/blog/channels/" + channel.ID.String(),
		"/api/v1/blog/channels/" + channel.ID.String() + "/collections",
		"/api/v1/blog/channels/slug/" + channel.Slug,
		"/api/v1/blog/channels/slug/" + channel.Slug + "/collections",
		"/api/v1/blog/collections/" + secondary.ID.String(),
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("expected route %s to be mounted, got 404: %s", path, w.Body.String())
		}
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/channels/ensure-default", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected ensure-default route to be mounted, got 404: %s", w.Body.String())
	}
}

func TestRegisterRoutesMountsChannelAndCollectionMutationEndpoints(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	r := newBlogHTTPRouter(service, &user)

	createChannelRaw := bytes.NewBufferString(`{"name":"Studio Channel","slug":"studio-channel","description":"desc"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/channels", createChannelRaw)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected create channel route to be mounted, got 404: %s", w.Body.String())
	}

	var createdChannel struct {
		Data model.Channel `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createdChannel); err != nil {
		t.Fatalf("decode create channel response: %v", err)
	}
	if createdChannel.Data.ID == uuid.Nil {
		t.Fatalf("expected created channel id, got %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/blog/channels/"+createdChannel.Data.ID.String(), bytes.NewBufferString(`{"name":"Studio Channel Updated","slug":"studio-channel-updated"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected update channel route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/blog/channels/"+createdChannel.Data.ID.String()+"/collections", bytes.NewBufferString(`{"name":"Issues","description":"desc"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected create collection route to be mounted, got 404: %s", w.Body.String())
	}

	var createdCollection struct {
		Data model.Collection `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createdCollection); err != nil {
		t.Fatalf("decode create collection response: %v", err)
	}
	if createdCollection.Data.ID == uuid.Nil {
		t.Fatalf("expected created collection id, got %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/blog/collections", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected list user collections route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/blog/collections/"+createdCollection.Data.ID.String(), bytes.NewBufferString(`{"name":"Issues Updated","description":"updated"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected update collection route to be mounted, got 404: %s", w.Body.String())
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.Model(&model.User{}).Where("uuid = ?", user.ID).Update("password", string(passwordHash)).Error; err != nil {
		t.Fatalf("update password: %v", err)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/blog/channels/"+createdChannel.Data.ID.String(), bytes.NewBufferString(`{"password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected delete channel route to be mounted, got 404: %s", w.Body.String())
	}
}

func TestRegisterRoutesMountsChannelArticleRSS(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Published", Content: "Body", Summary: "Summary", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create published post: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/channels/slug/"+channel.Slug+"/rss/article", nil)
	req.Host = "example.com"
	r.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Fatalf("expected article rss route to be mounted, got 404: %s", w.Body.String())
	}
	if contentType := w.Header().Get("Content-Type"); !strings.Contains(contentType, "application/rss+xml") {
		t.Fatalf("expected rss content-type, got %q", contentType)
	}
	if !strings.Contains(w.Body.String(), "<rss") {
		t.Fatalf("expected rss body, got %s", w.Body.String())
	}
}

func TestRegisterRoutesMountsBookmarkAndLikeReadEndpoints(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Published", Content: "Body", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String()+"/likes/count", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected likes count route to be mounted, got 404: %s", w.Body.String())
	}

	folder := model.BookmarkFolder{UserID: user.ID, Name: "Favorites"}
	if err := db.Create(&folder).Error; err != nil {
		t.Fatalf("create bookmark folder: %v", err)
	}
	bookmark := model.Bookmark{UserID: user.ID, PostID: post.ID, BookmarkFolderID: &folder.ID}
	if err := db.Create(&bookmark).Error; err != nil {
		t.Fatalf("create bookmark: %v", err)
	}

	for _, path := range []string{
		"/api/v1/blog/bookmarks",
		"/api/v1/blog/bookmarks?folder_id=" + folder.ID.String(),
		"/api/v1/blog/bookmark-folders",
	} {
		w = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("expected route %s to be mounted, got 404: %s", path, w.Body.String())
		}
	}
}

func TestRegisterRoutesMountsBlogRecommendationPostsEndpoint(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}

	post := model.Post{
		UserID:             user.ID,
		ChannelID:          &channel.ID,
		Title:              "推荐文章",
		Content:            "这是一篇适合推荐的文章内容。",
		Summary:            "推荐摘要",
		Status:             "published",
		Visibility:         "public",
		RatingAverageScore: 86,
		RatingCount:        12,
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/recommend/posts?mode=hot&page=1&page_size=20", nil)
	r.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Fatalf("expected recommendation route to be mounted, got 404: %s", w.Body.String())
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data []struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			Summary    string `json:"summary"`
			ContentType string `json:"content_type"`
			TargetPath string `json:"target_path"`
			ScoreLabel string `json:"score_label"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected recommendation items, got %s", w.Body.String())
	}
	first := payload.Data[0]
	if first.ID == "" || first.Title == "" || first.TargetPath == "" || first.ScoreLabel == "" || first.ContentType != "blog" {
		t.Fatalf("expected recommendation dto fields, got %#v", first)
	}
}

func TestRegisterRoutesMountsBookmarkAndFolderMutationEndpoints(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Published", Content: "Body", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	post2 := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Published 2", Content: "Body", Status: "published", Visibility: "public"}
	if err := db.Create(&post2).Error; err != nil {
		t.Fatalf("create second post: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/bookmark-folders", bytes.NewBufferString(`{"name":"Favorites"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected create folder route to be mounted, got 404: %s", w.Body.String())
	}

	var folderResp struct {
		Data model.BookmarkFolder `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &folderResp); err != nil {
		t.Fatalf("decode folder response: %v", err)
	}
	if folderResp.Data.ID == uuid.Nil {
		t.Fatalf("expected bookmark folder id, got %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/blog/bookmarks", bytes.NewBufferString(`{"post_id":"`+post2.ID.String()+`","bookmark_folder_id":"`+folderResp.Data.ID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected create bookmark route to be mounted, got 404: %s", w.Body.String())
	}

	var bookmarkResp struct {
		Data model.Bookmark `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &bookmarkResp); err != nil {
		t.Fatalf("decode bookmark response: %v", err)
	}
	if bookmarkResp.Data.ID == uuid.Nil {
		t.Fatalf("expected bookmark id, got %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/blog/bookmarks", bytes.NewBufferString(`{"post_id":"`+post.ID.String()+`","bookmark_folder_id":"`+folderResp.Data.ID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code >= http.StatusBadRequest {
		t.Fatalf("expected second bookmark create to succeed or be idempotent, got %d: %s", w.Code, w.Body.String())
	}

	var bookmarkForFolderResp struct {
		Data model.Bookmark `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &bookmarkForFolderResp); err != nil {
		t.Fatalf("decode second bookmark response: %v", err)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/blog/bookmarks/"+bookmarkResp.Data.ID.String(), nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected delete bookmark route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/blog/bookmark-folders/"+folderResp.Data.ID.String(), nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected delete folder route to be mounted, got 404: %s", w.Body.String())
	}

	var remainingBookmark model.Bookmark
	if err := db.Unscoped().First(&remainingBookmark, "id = ?", bookmarkForFolderResp.Data.ID).Error; err != nil {
		t.Fatalf("reload bookmark: %v", err)
	}
	if remainingBookmark.BookmarkFolderID != nil {
		t.Fatalf("expected bookmark folder to be cleared after folder delete, got %#v", remainingBookmark.BookmarkFolderID)
	}
}

func TestRegisterRoutesMountsCommentAndLikeMutationEndpoints(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Published", Content: "Body", Status: "published", Visibility: "public", AllowComments: true}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)

	for _, path := range []string{
		"/api/v1/blog/posts/" + post.ID.String() + "/comments",
		"/api/v1/blog/posts/" + post.ID.String() + "/likes/count",
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("expected route %s to be mounted, got 404: %s", path, w.Body.String())
		}
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/comments", bytes.NewBufferString(`{"content":"Nice post"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected create comment route to be mounted, got 404: %s", w.Body.String())
	}

	var createdComment struct {
		Data model.Comment `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createdComment); err != nil {
		t.Fatalf("decode comment response: %v", err)
	}
	if createdComment.Data.ID == uuid.Nil {
		t.Fatalf("expected comment id, got %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/blog/likes", bytes.NewBufferString(`{"target_type":"post","target_id":"`+post.ID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected like route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/blog/likes", bytes.NewBufferString(`{"target_type":"post","target_id":"`+post.ID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected unlike route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/blog/comments/"+createdComment.Data.ID.String(), nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected delete comment route to be mounted, got 404: %s", w.Body.String())
	}
}

func TestCreateDefaultChannelForUserSkipsReservedAndUserHandles(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	other := model.User{Username: "design", Email: "design@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}

	reservedChannel, err := service.CreateDefaultChannelForUser(user.ID, "feed")
	if err != nil {
		t.Fatalf("create reserved-name channel: %v", err)
	}
	if reservedChannel.Slug == "feed" {
		t.Fatalf("expected reserved feed slug to be skipped")
	}

	userChannel, err := service.CreateDefaultChannelForUser(other.UUID, "design")
	if err != nil {
		t.Fatalf("create username-colliding channel: %v", err)
	}
	if userChannel.Slug == "design" {
		t.Fatalf("expected username-colliding slug to be skipped")
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
		"title":      "HTTP Post",
		"content":    "content",
		"excerpt":    "summary",
		"cover_url":  "https://example.com/cover.png",
		"channel_id": channel.ID,
		"visibility": "public",
		"status":     "draft",
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

	var collections []model.Collection
	if err := service.db.Model(&resp.Data).Association("Collections").Find(&collections); err != nil {
		t.Fatalf("load post collections: %v", err)
	}
	if len(collections) != 1 || collections[0].ID != collection.ID {
		t.Fatalf("expected created post to be assigned to default collection %s, got %#v", collection.ID, collections)
	}
}

func TestCreatePostRollsBackWhenCollectionAssociationFails(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	if err := db.Exec(`
		CREATE TRIGGER fail_post_collection_insert
		BEFORE INSERT ON post_collections
		BEGIN
			SELECT RAISE(FAIL, 'post collection failed');
		END;
	`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	_, err = service.CreatePost(user, CreatePostRequest{
		Title:     "Should Roll Back",
		Content:   "content",
		ChannelID: channel.ID,
		Status:    "draft",
	})
	if err == nil {
		t.Fatal("expected create post to fail")
	}

	var count int64
	if err := db.Model(&model.Post{}).Where("title = ?", "Should Roll Back").Count(&count).Error; err != nil {
		t.Fatalf("count posts: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected post insert rolled back, count=%d", count)
	}
}

func TestRegisterRoutesCreatePostAcceptsSummaryField(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	body := map[string]any{
		"title":      "HTTP Post",
		"content":    "content",
		"summary":    "summary from frontend",
		"channel_id": channel.ID,
		"visibility": "public",
		"status":     "draft",
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
	if resp.Data.Summary != "summary from frontend" {
		t.Fatalf("expected created post summary from summary field, got %#v", resp.Data.Summary)
	}
}

func TestRegisterRoutesListPostsReturnsPublishedPosts(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	if err := db.Create(&model.Post{UserID: user.ID, Title: "Published", Content: "body", Status: "published", Visibility: "public"}).Error; err != nil {
		t.Fatalf("create published post: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterRoutesGetPostRejectsPrivatePostForNonOwner(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	viewer := model.User{Username: "viewer", Email: "viewer@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&viewer).Error; err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	channel, err := service.CreateDefaultChannelForUser(owner.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	post, err := service.CreatePost(owner, CreatePostRequest{Title: "Secret", Content: "body", ChannelID: channel.ID, Visibility: "private", Status: "published"})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	r := newBlogHTTPRouter(service, &authctx.CurrentUser{ID: viewer.UUID, Username: viewer.Username, Role: authctx.RoleUser})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String(), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func createOwnedChannelAndCollection(t *testing.T, service *Service, user authctx.CurrentUser, name string) (model.Channel, model.Collection) {
	t.Helper()

	channel, err := service.CreateDefaultChannelForUser(user.ID, name)
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}

	var collection model.Collection
	if err := service.db.Where("channel_id = ? AND is_default = ?", channel.ID, true).First(&collection).Error; err != nil {
		t.Fatalf("load default collection: %v", err)
	}

	return channel, collection
}

func createPostRecord(t *testing.T, db *gorm.DB, userID uuid.UUID, channelID *uuid.UUID, title, status string) model.Post {
	t.Helper()

	post := model.Post{UserID: userID, ChannelID: channelID, Title: title, Content: "content", Status: status, Visibility: "public", AllowComments: true}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	return post
}

type testBlogDraftResponse struct {
	ContextKey    string   `json:"context_key"`
	Visibility    string   `json:"visibility"`
	AllowComments bool     `json:"allow_comments"`
	CollectionIDs []string `json:"collection_ids"`
}

func decodePostResponse(t *testing.T, body []byte) model.Post {
	t.Helper()

	var resp struct {
		Data model.Post `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.Data
}

func TestRegisterRoutesUpdatePostUpdatesOwnedPost(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, defaultCollection := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Before", "draft")

	secondary := model.Collection{ChannelID: channel.ID, Name: "Featured", Description: "featured"}
	if err := db.Create(&secondary).Error; err != nil {
		t.Fatalf("create secondary collection: %v", err)
	}
	if err := db.Model(&post).Association("Collections").Append(&defaultCollection); err != nil {
		t.Fatalf("attach default collection: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	body := map[string]any{
		"title":          "After",
		"content":        "updated body",
		"summary":        "updated summary",
		"cover_url":      "https://example.com/updated.png",
		"visibility":     "followers",
		"allow_comments": false,
		"status":         "published",
		"channel_id":     channel.ID.String(),
		"collection_ids": []string{secondary.ID.String()},
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	updated := decodePostResponse(t, w.Body.Bytes())
	if updated.Title != "After" || updated.Status != "published" || updated.Visibility != "followers" || updated.AllowComments {
		t.Fatalf("unexpected updated response: %#v", updated)
	}
	if len(updated.Collections) != 2 {
		t.Fatalf("expected default and selected collection, got %#v", updated.Collections)
	}
}

func TestRegisterRoutesUpdatePostRollsBackWhenCollectionAssociationFails(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, defaultCollection := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Before", "draft")
	if err := db.Model(&post).Association("Collections").Append(&defaultCollection); err != nil {
		t.Fatalf("attach default collection: %v", err)
	}
	secondary := model.Collection{ChannelID: channel.ID, Name: "Featured", Description: "featured"}
	if err := db.Create(&secondary).Error; err != nil {
		t.Fatalf("create secondary collection: %v", err)
	}
	if err := db.Exec(`
		CREATE TRIGGER fail_post_collection_insert
		BEFORE INSERT ON post_collections
		BEGIN
			SELECT RAISE(FAIL, 'post collection failed');
		END;
	`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	body := map[string]any{
		"title":          "After",
		"content":        "updated body",
		"channel_id":     channel.ID.String(),
		"collection_ids": []string{secondary.ID.String()},
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("expected update to fail, got %d: %s", w.Code, w.Body.String())
	}

	var reloaded model.Post
	if err := db.Preload("Collections").First(&reloaded, "id = ?", post.ID).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if reloaded.Title != "Before" {
		t.Fatalf("expected post update rolled back, got title %q", reloaded.Title)
	}
	if len(reloaded.Collections) != 1 || reloaded.Collections[0].ID != defaultCollection.ID {
		t.Fatalf("expected old collection association preserved, got %#v", reloaded.Collections)
	}
}

func TestRegisterRoutesUpdatePostForbidsNonOwner(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, owner, "Alice")
	post := createPostRecord(t, db, owner.ID, &channel.ID, "Before", "draft")
	viewer := model.User{Username: "viewer2", Email: "viewer2@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&viewer).Error; err != nil {
		t.Fatalf("create viewer: %v", err)
	}

	r := newBlogHTTPRouter(service, &authctx.CurrentUser{ID: viewer.UUID, Username: viewer.Username, Role: authctx.RoleUser})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), bytes.NewBufferString(`{"title":"x","content":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterRoutesDeletePostDeletesOwnedPost(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Delete me", "draft")

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/blog/posts/"+post.ID.String(), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var count int64
	if err := db.Model(&model.Post{}).Where("id = ?", post.ID).Count(&count).Error; err != nil {
		t.Fatalf("count posts: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected post deleted, count=%d", count)
	}
}

func TestRegisterRoutesDeletePostForbidsNonOwner(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, owner, "Alice")
	post := createPostRecord(t, db, owner.ID, &channel.ID, "Delete me", "draft")
	viewer := model.User{Username: "viewer3", Email: "viewer3@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&viewer).Error; err != nil {
		t.Fatalf("create viewer: %v", err)
	}

	r := newBlogHTTPRouter(service, &authctx.CurrentUser{ID: viewer.UUID, Username: viewer.Username, Role: authctx.RoleUser})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/blog/posts/"+post.ID.String(), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterRoutesPublishPostUpdatesStatus(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Publish me", "draft")

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/publish", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated model.Post
	if err := db.First(&updated, "id = ?", post.ID).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if updated.Status != "published" {
		t.Fatalf("expected published, got %s", updated.Status)
	}
}

func TestRegisterRoutesUnpublishPostUpdatesStatus(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Unpublish me", "published")

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/unpublish", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated model.Post
	if err := db.First(&updated, "id = ?", post.ID).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if updated.Status != "draft" {
		t.Fatalf("expected draft, got %s", updated.Status)
	}
}

func TestRegisterRoutesPinPostUpdatesPinnedState(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Pin me", "published")

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/pin", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated model.Post
	if err := db.First(&updated, "id = ?", post.ID).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if !updated.Pinned {
		t.Fatal("expected post pinned")
	}
}

func TestRegisterRoutesUnpinPostUpdatesPinnedState(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Unpin me", "published")
	if err := db.Model(&post).Update("pinned", true).Error; err != nil {
		t.Fatalf("preset pinned: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/unpin", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated model.Post
	if err := db.First(&updated, "id = ?", post.ID).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if updated.Pinned {
		t.Fatal("expected post unpinned")
	}
}

func TestRegisterRoutesGetDraftsReturnsUserDrafts(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, _ := createOwnedChannelAndCollection(t, service, user, "Alice")
	_ = createPostRecord(t, db, user.ID, &channel.ID, "Draft one", "draft")
	_ = createPostRecord(t, db, user.ID, &channel.ID, "Published one", "published")

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/drafts", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []model.Post `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Status != "draft" {
		t.Fatalf("unexpected drafts response: %#v", resp.Data)
	}
}

func TestRegisterRoutesGetBlogDraftReturnsSavedDraft(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	draft := model.BlogDraft{UserID: user.ID, ContextKey: "editor:1", Title: "Saved", Content: "body", Visibility: "followers", AllowComments: false}
	if err := db.Create(&draft).Error; err != nil {
		t.Fatalf("create draft: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/drafts?context_key=editor:1", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data testBlogDraftResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.ContextKey != "editor:1" || resp.Data.Visibility != "followers" {
		t.Fatalf("unexpected blog draft response: %#v", resp.Data)
	}
}

func TestRegisterRoutesPutBlogDraftPersistsFollowersVisibility(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	r := newBlogHTTPRouter(service, &user)
	body := `{"context_key":"editor:2","title":"Draft","content":"body","visibility":"followers","allow_comments":false}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/drafts", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data testBlogDraftResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Visibility != "followers" {
		t.Fatalf("unexpected saved draft: %#v", resp.Data)
	}
}

func TestRegisterRoutesDeleteBlogDraftRemovesSavedDraft(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	draft := model.BlogDraft{UserID: user.ID, ContextKey: "editor:3", Title: "Saved", Content: "body"}
	if err := db.Create(&draft).Error; err != nil {
		t.Fatalf("create draft: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/blog/drafts?context_key=editor:3", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %s", w.Body.String())
	}

	var count int64
	if err := db.Model(&model.BlogDraft{}).Where("user_id = ? AND context_key = ?", user.ID, "editor:3").Count(&count).Error; err != nil {
		t.Fatalf("count drafts: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected draft deleted, count=%d", count)
	}
}

func TestRegisterRoutesAddPostToCollectionAddsAssociation(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, defaultCollection := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Collect me", "draft")
	if err := db.Model(&post).Association("Collections").Append(&defaultCollection); err != nil {
		t.Fatalf("attach default collection: %v", err)
	}
	secondary := model.Collection{ChannelID: channel.ID, Name: "Weekly", Description: "weekly"}
	if err := db.Create(&secondary).Error; err != nil {
		t.Fatalf("create collection: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	body := map[string]string{"collection_id": secondary.ID.String()}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/collections", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var reloaded model.Post
	if err := db.Preload("Collections").First(&reloaded, "id = ?", post.ID).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if len(reloaded.Collections) != 2 {
		t.Fatalf("expected 2 collections, got %#v", reloaded.Collections)
	}
}

func TestRegisterRoutesRemovePostFromCollectionRemovesAssociation(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, defaultCollection := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Collect me", "draft")
	secondary := model.Collection{ChannelID: channel.ID, Name: "Monthly", Description: "monthly"}
	if err := db.Create(&secondary).Error; err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if err := db.Model(&post).Association("Collections").Append(&defaultCollection, &secondary); err != nil {
		t.Fatalf("attach collections: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/blog/posts/"+post.ID.String()+"/collections/"+secondary.ID.String(), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var reloaded model.Post
	if err := db.Preload("Collections").First(&reloaded, "id = ?", post.ID).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if len(reloaded.Collections) != 1 || reloaded.Collections[0].ID != defaultCollection.ID {
		t.Fatalf("unexpected collections after remove: %#v", reloaded.Collections)
	}
}

func TestRegisterRoutesReorderCollectionPostsPersistsPosition(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, defaultCollection := createOwnedChannelAndCollection(t, service, user, "Alice")
	postA := createPostRecord(t, db, user.ID, &channel.ID, "Post A", "draft")
	postB := createPostRecord(t, db, user.ID, &channel.ID, "Post B", "published")
	postC := createPostRecord(t, db, user.ID, &channel.ID, "Post C", "draft")

	if err := db.Create(&model.PostCollection{PostID: postA.ID, CollectionID: defaultCollection.ID, Position: 0}).Error; err != nil {
		t.Fatalf("attach post A: %v", err)
	}
	if err := db.Create(&model.PostCollection{PostID: postB.ID, CollectionID: defaultCollection.ID, Position: 1}).Error; err != nil {
		t.Fatalf("attach post B: %v", err)
	}
	if err := db.Create(&model.PostCollection{PostID: postC.ID, CollectionID: defaultCollection.ID, Position: 2}).Error; err != nil {
		t.Fatalf("attach post C: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	body := map[string]any{
		"post_ids": []string{postC.ID.String(), postA.ID.String(), postB.ID.String()},
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/collections/"+defaultCollection.ID.String()+"/posts/order", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var links []model.PostCollection
	if err := db.Where("collection_id = ?", defaultCollection.ID).Order("position ASC").Find(&links).Error; err != nil {
		t.Fatalf("reload positions: %v", err)
	}
	if len(links) != 3 {
		t.Fatalf("expected 3 post links, got %d", len(links))
	}
	got := []uuid.UUID{links[0].PostID, links[1].PostID, links[2].PostID}
	want := []uuid.UUID{postC.ID, postA.ID, postB.ID}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected order %v, got %v", want, got)
		}
		if links[i].Position != i {
			t.Fatalf("expected position %d at index %d, got %d", i, i, links[i].Position)
		}
	}
}
