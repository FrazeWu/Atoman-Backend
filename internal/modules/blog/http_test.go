package blog

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		&model.UserDefaultChannel{},
		&model.Collection{},
		&model.Post{},
		&model.PostCollection{},
		&model.BlogPostVersion{},
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
	missingFolderW := httptest.NewRecorder()
	missingFolderReq := httptest.NewRequest(http.MethodPost, "/api/v1/blog/bookmarks", bytes.NewBufferString(`{"post_id":"`+post.ID.String()+`"}`))
	missingFolderReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(missingFolderW, missingFolderReq)
	if missingFolderW.Code != http.StatusBadRequest {
		t.Fatalf("expected bookmark folder to be required, got %d: %s", missingFolderW.Code, missingFolderW.Body.String())
	}

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
		UserID:     user.ID,
		ChannelID:  &channel.ID,
		Title:      "推荐文章",
		Content:    "这是一篇适合推荐的文章内容。",
		Summary:    "推荐摘要",
		Status:     "published",
		Visibility: "public",
		ViewCount:  86,
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
			ID          string `json:"id"`
			Title       string `json:"title"`
			Summary     string `json:"summary"`
			ContentType string `json:"content_type"`
			TargetPath  string `json:"target_path"`
			ScoreLabel  string `json:"score_label"`
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
	if remainingBookmark.BookmarkFolderID == nil || *remainingBookmark.BookmarkFolderID == folderResp.Data.ID {
		t.Fatalf("expected bookmark to move to default folder, got %#v", remainingBookmark.BookmarkFolderID)
	}
	var fallback model.BookmarkFolder
	if err := db.First(&fallback, "id = ?", *remainingBookmark.BookmarkFolderID).Error; err != nil || fallback.Name != "默认收藏夹" {
		t.Fatalf("expected default fallback folder, got %#v err=%v", fallback, err)
	}
}

func TestBlogBookmarksSupportPopularSort(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	hotPost := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Hot", Content: "Body", Status: "published", Visibility: "public"}
	coldPost := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Cold", Content: "Body", Status: "published", Visibility: "public"}
	if err := db.Create(&hotPost).Error; err != nil {
		t.Fatalf("create hot post: %v", err)
	}
	if err := db.Create(&coldPost).Error; err != nil {
		t.Fatalf("create cold post: %v", err)
	}
	if err := db.Create(&model.Bookmark{UserID: user.ID, PostID: coldPost.ID}).Error; err != nil {
		t.Fatalf("create cold bookmark: %v", err)
	}
	if err := db.Create(&model.Bookmark{UserID: user.ID, PostID: hotPost.ID}).Error; err != nil {
		t.Fatalf("create hot bookmark: %v", err)
	}
	if err := db.Create(&model.Like{UserID: user.ID, TargetType: "post", TargetID: hotPost.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/bookmarks?sort=popular", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			PostID string `json:"post_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) < 2 {
		t.Fatalf("expected 2 bookmarks, got %s", w.Body.String())
	}
	if resp.Data[0].PostID != hotPost.ID.String() {
		t.Fatalf("expected hot post first, got %#v", resp.Data)
	}
}

func TestCreateBookmarkMovesExistingBookmarkToSelectedFolder(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "Move Bookmark")
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, CollectionID: &collection.ID, Title: "Post", Content: "Body", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	first := model.BookmarkFolder{UserID: user.ID, Name: "First"}
	second := model.BookmarkFolder{UserID: user.ID, Name: "Second"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first folder: %v", err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second folder: %v", err)
	}
	if _, err := service.CreateBookmark(user, post.ID, &first.ID); err != nil {
		t.Fatalf("create bookmark: %v", err)
	}
	bookmark, err := service.CreateBookmark(user, post.ID, &second.ID)
	if err != nil {
		t.Fatalf("move bookmark: %v", err)
	}
	if bookmark.BookmarkFolderID == nil || *bookmark.BookmarkFolderID != second.ID {
		t.Fatalf("expected second folder, got %#v", bookmark.BookmarkFolderID)
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

func TestCreateDefaultChannelForUserPersistsBlogDefaultSelection(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)

	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}

	if channel.ContentType != "blog" {
		t.Fatalf("expected blog content type, got %q", channel.ContentType)
	}

	var selection model.UserDefaultChannel
	if err := db.Where("user_id = ? AND content_type = ?", user.ID, "blog").First(&selection).Error; err != nil {
		t.Fatalf("load default selection: %v", err)
	}
	if selection.ChannelID != channel.ID {
		t.Fatalf("expected selected channel %s, got %s", channel.ID, selection.ChannelID)
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

func TestRegisterRoutesDoesNotMountPostRating(t *testing.T) {
	service, _, user := newBlogHTTPTestService(t)
	r := newBlogHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+uuid.NewString()+"/rating", bytes.NewBufferString(`{"score":8}`))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected rating route to be absent, got %d: %s", w.Code, w.Body.String())
	}
	if strings.TrimSpace(w.Body.String()) != "404 page not found" {
		t.Fatalf("expected router 404, got %s", w.Body.String())
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
		"title":         "HTTP Post",
		"content":       "content",
		"excerpt":       "summary",
		"cover_url":     "https://example.com/cover.png",
		"collection_id": collection.ID,
		"visibility":    "public",
		"status":        "draft",
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

	if resp.Data.CollectionID == nil || *resp.Data.CollectionID != collection.ID {
		t.Fatalf("expected created post to be assigned to collection %s, got %#v", collection.ID, resp.Data.CollectionID)
	}
}

func TestRegisterRoutesCreatePostRequiresExactlyOneOwnedCollection(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, defaultCollection := createOwnedChannelAndCollection(t, service, user, "Single Collection")
	secondary := model.Collection{ChannelID: channel.ID, CreatedBy: &user.ID, Name: "Secondary"}
	if err := db.Create(&secondary).Error; err != nil {
		t.Fatalf("create secondary collection: %v", err)
	}
	r := newBlogHTTPRouter(service, &user)

	multipleBody, _ := json.Marshal(map[string]any{
		"title":          "Invalid",
		"content":        "body",
		"channel_id":     channel.ID,
		"collection_ids": []uuid.UUID{defaultCollection.ID, secondary.ID},
		"status":         "draft",
	})
	multipleReq := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts", bytes.NewReader(multipleBody))
	multipleReq.Header.Set("Content-Type", "application/json")
	multipleW := httptest.NewRecorder()
	r.ServeHTTP(multipleW, multipleReq)
	if multipleW.Code != http.StatusBadRequest {
		t.Fatalf("expected legacy multi-collection input to be rejected, got %d: %s", multipleW.Code, multipleW.Body.String())
	}

	validBody, _ := json.Marshal(map[string]any{
		"title":         "Valid",
		"content":       "body",
		"collection_id": secondary.ID,
		"status":        "draft",
	})
	validReq := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts", bytes.NewReader(validBody))
	validReq.Header.Set("Content-Type", "application/json")
	validW := httptest.NewRecorder()
	r.ServeHTTP(validW, validReq)
	if validW.Code != http.StatusCreated {
		t.Fatalf("expected single collection create to succeed, got %d: %s", validW.Code, validW.Body.String())
	}
	var response struct {
		Data struct {
			CollectionID uuid.UUID `json:"collection_id"`
			ChannelID    uuid.UUID `json:"channel_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(validW.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.CollectionID != secondary.ID || response.Data.ChannelID != channel.ID {
		t.Fatalf("expected collection %s and derived channel %s, got %s", secondary.ID, channel.ID, validW.Body.String())
	}
}

func TestCreatePublishedPostRollsBackWhenVersionSnapshotFails(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	_, collection := createOwnedChannelAndCollection(t, service, user, "Alice")
	if err := db.Exec(`
		CREATE TRIGGER fail_blog_post_version_insert
		BEFORE INSERT ON blog_post_versions
		BEGIN
			SELECT RAISE(FAIL, 'version failed');
		END;
	`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	_, err := service.CreatePost(user, CreatePostRequest{
		Title:        "Should Roll Back",
		Content:      "content",
		CollectionID: collection.ID,
		Status:       "published",
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
	_, collection := createOwnedChannelAndCollection(t, service, user, "Alice")

	r := newBlogHTTPRouter(service, &user)
	body := map[string]any{
		"title":         "HTTP Post",
		"content":       "content",
		"summary":       "summary from frontend",
		"collection_id": collection.ID,
		"visibility":    "public",
		"status":        "draft",
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

func TestRegisterRoutesListPostsOrdersLatestByFirstPublishedAt(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	earlyCreated := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	lateCreated := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	earlyPublished := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	latePublished := time.Date(2026, 7, 5, 8, 0, 0, 0, time.UTC)
	first := model.Post{Base: model.Base{ID: uuid.New(), CreatedAt: earlyCreated}, UserID: user.ID, Title: "Early created, late published", Content: "body", Status: "published", Visibility: "public", PublishedAt: &earlyPublished}
	second := model.Post{Base: model.Base{ID: uuid.New(), CreatedAt: lateCreated}, UserID: user.ID, Title: "Late created, early published", Content: "body", Status: "published", Visibility: "public", PublishedAt: &latePublished}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first post: %v", err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second post: %v", err)
	}

	r := newBlogHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts?page=1&page_size=20", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data []model.Post `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 2 || response.Data[0].ID != first.ID {
		t.Fatalf("expected late-published post first, got %s", w.Body.String())
	}
}

func TestRegisterRoutesListPostsReturnsPagedFlatDTOWithInteractionCounts(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	for _, title := range []string{"Needle One", "Needle Two", "Needle Three"} {
		if err := db.Create(&model.Post{UserID: user.ID, Title: title, Content: "body", Status: "published", Visibility: "public"}).Error; err != nil {
			t.Fatalf("create published post: %v", err)
		}
	}
	var newest model.Post
	if err := db.Where("title = ?", "Needle Three").First(&newest).Error; err != nil {
		t.Fatalf("load newest post: %v", err)
	}
	if err := db.Create(&model.Like{UserID: user.ID, TargetType: "post", TargetID: newest.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Create(&model.Comment{TargetType: "post", TargetID: newest.ID, UserID: model.NullableUserUUID{UUID: user.ID, Valid: true}, Content: "comment", Status: "visible"}).Error; err != nil {
		t.Fatalf("create comment: %v", err)
	}

	r := newBlogHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts?page=1&page_size=2&q=Needle", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data []struct {
			ID            uuid.UUID `json:"id"`
			Title         string    `json:"title"`
			LikesCount    int64     `json:"likes_count"`
			CommentsCount int64     `json:"comments_count"`
		} `json:"data"`
		Meta struct {
			Page     int   `json:"page"`
			PageSize int   `json:"page_size"`
			Total    int64 `json:"total"`
			HasMore  bool  `json:"has_more"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 2 || response.Meta.Page != 1 || response.Meta.PageSize != 2 || response.Meta.Total != 3 || !response.Meta.HasMore {
		t.Fatalf("unexpected paged response: %s", w.Body.String())
	}
	if response.Data[0].ID != newest.ID || response.Data[0].LikesCount != 1 || response.Data[0].CommentsCount != 1 {
		t.Fatalf("expected flat newest post DTO with counts, got %s", w.Body.String())
	}
}

func TestRegisterRoutesListPostsHidesNonPublicPostsFromAnonymousViewer(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	for _, input := range []struct {
		title      string
		visibility string
	}{
		{title: "Public", visibility: "public"},
		{title: "Private", visibility: "private"},
		{title: "Followers", visibility: "followers"},
	} {
		if err := db.Create(&model.Post{UserID: user.ID, Title: input.title, Content: "body", Status: "published", Visibility: input.visibility}).Error; err != nil {
			t.Fatalf("create post: %v", err)
		}
	}

	r := newBlogHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts?page=1&page_size=20", nil)
	r.ServeHTTP(w, req)

	var response struct {
		Data []model.Post `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].Title != "Public" || response.Meta.Total != 1 {
		t.Fatalf("expected only public post, got %s", w.Body.String())
	}
}

func TestRegisterRoutesGetPostRejectsPrivatePostForNonOwner(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	viewer := model.User{Username: "viewer", Email: "viewer@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&viewer).Error; err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	_, collection := createOwnedChannelAndCollection(t, service, owner, "Alice")
	post, err := service.CreatePost(owner, CreatePostRequest{Title: "Secret", Content: "body", CollectionID: collection.ID, Visibility: "private", Status: "published"})
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

func TestRegisterRoutesGetPostReturnsViewerLikeState(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create default channel: %v", err)
	}
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Published", Content: "Body", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	if err := db.Create(&model.Like{UserID: user.ID, TargetType: "post", TargetID: post.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String(), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Data struct {
			ID         uuid.UUID `json:"id"`
			Liked      bool      `json:"liked"`
			LikesCount int64     `json:"likes_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode get post response: %v", err)
	}
	if response.Data.ID != post.ID {
		t.Fatalf("expected post id %s, got %s", post.ID, response.Data.ID)
	}
	if !response.Data.Liked {
		t.Fatalf("expected liked=true, got false: %s", w.Body.String())
	}
	if response.Data.LikesCount != 1 {
		t.Fatalf("expected likes_count=1, got %d: %s", response.Data.LikesCount, w.Body.String())
	}
}

func TestRegisterRoutesGetPostReturnsPublicStatsAndCountsReaderView(t *testing.T) {
	service, db, owner := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, owner, "Stats")
	post := model.Post{
		UserID: owner.ID, ChannelID: &channel.ID, CollectionID: &collection.ID,
		Title: "Stats", Content: "Body", Status: "published", Visibility: "public", ViewCount: 3,
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	if err := db.Create(&model.Comment{TargetType: "post", TargetID: post.ID, UserID: model.NullableUserUUID{UUID: owner.ID, Valid: true}, Content: "comment", Status: "visible"}).Error; err != nil {
		t.Fatalf("create comment: %v", err)
	}
	if err := db.Create(&model.Bookmark{UserID: owner.ID, PostID: post.ID}).Error; err != nil {
		t.Fatalf("create bookmark: %v", err)
	}
	source := model.FeedSource{SourceType: "internal_channel", SourceID: &channel.ID, Provider: "internal", Category: "blog", Hash: uuid.NewString(), Title: channel.Name}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create feed source: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: owner.ID, FeedSourceID: source.ID}).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	r := newBlogHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String(), nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			ViewCount             int64 `json:"view_count"`
			CommentsCount         int64 `json:"comments_count"`
			BookmarksCount        int64 `json:"bookmarks_count"`
			ChannelFollowersCount int64 `json:"channel_followers_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.ViewCount != 4 || response.Data.CommentsCount != 1 || response.Data.BookmarksCount != 1 || response.Data.ChannelFollowersCount != 1 {
		t.Fatalf("unexpected stats: %s", w.Body.String())
	}

	ownerRouter := newBlogHTTPRouter(service, &owner)
	ownerW := httptest.NewRecorder()
	ownerRouter.ServeHTTP(ownerW, httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String(), nil))
	var reloaded model.Post
	if err := db.First(&reloaded, "id = ?", post.ID).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if reloaded.ViewCount != 4 {
		t.Fatalf("expected owner view not to increment, got %d", reloaded.ViewCount)
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
	ContextKey    string  `json:"context_key"`
	Visibility    string  `json:"visibility"`
	AllowComments bool    `json:"allow_comments"`
	CollectionID  *string `json:"collection_id"`
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
	post.CollectionID = &defaultCollection.ID
	if err := db.Save(&post).Error; err != nil {
		t.Fatalf("assign default collection: %v", err)
	}

	secondary := model.Collection{ChannelID: channel.ID, Name: "Featured", Description: "featured"}
	if err := db.Create(&secondary).Error; err != nil {
		t.Fatalf("create secondary collection: %v", err)
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
		"collection_id":  secondary.ID.String(),
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
	if updated.CollectionID == nil || *updated.CollectionID != secondary.ID {
		t.Fatalf("expected selected collection, got %#v", updated.CollectionID)
	}
}

func TestRegisterRoutesUpdatePostRejectsForeignCollectionWithoutChangingPost(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, defaultCollection := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Before", "draft")
	post.CollectionID = &defaultCollection.ID
	if err := db.Save(&post).Error; err != nil {
		t.Fatalf("assign default collection: %v", err)
	}
	other := model.User{Username: "other-collection-owner", Email: "other-collection@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other owner: %v", err)
	}
	_, foreignCollection := createOwnedChannelAndCollection(t, service, authctx.CurrentUser{ID: other.UUID, Username: other.Username, Role: authctx.RoleUser}, "Other")

	r := newBlogHTTPRouter(service, &user)
	body := map[string]any{"title": "After", "content": "updated body", "collection_id": foreignCollection.ID.String()}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected foreign collection update to be forbidden, got %d: %s", w.Code, w.Body.String())
	}
	var reloaded model.Post
	if err := db.First(&reloaded, "id = ?", post.ID).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if reloaded.Title != "Before" || reloaded.CollectionID == nil || *reloaded.CollectionID != defaultCollection.ID {
		t.Fatalf("expected post unchanged, got %#v", reloaded)
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
	_, collection := createOwnedChannelAndCollection(t, service, user, "Alice")
	post, err := service.CreatePost(user, CreatePostRequest{Title: "Publish me", Content: "body", CollectionID: collection.ID, Status: "draft", Visibility: "public"})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

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
	if updated.Status != "published" || updated.PublishedAt == nil {
		t.Fatalf("expected published with published_at, got %#v", updated)
	}
	var versionCount int64
	if err := db.Model(&model.BlogPostVersion{}).Where("post_id = ?", post.ID).Count(&versionCount).Error; err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("expected first published version, got %d", versionCount)
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

func TestPublishedPostVersionsPreservePublishedAtAndRestore(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "Versioned")
	post, err := service.CreatePost(user, CreatePostRequest{
		Title: "Version One", Content: "first body", CollectionID: collection.ID, Status: "published", Visibility: "public",
	})
	if err != nil {
		t.Fatalf("create published post: %v", err)
	}
	if post.PublishedAt == nil {
		t.Fatal("expected published_at on first publish")
	}
	firstPublishedAt := *post.PublishedAt

	r := newBlogHTTPRouter(service, &user)
	updateBody, _ := json.Marshal(map[string]any{
		"title": "Version Two", "content": "second body", "collection_id": collection.ID.String(), "status": "published",
	})
	updateW := httptest.NewRecorder()
	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), bytes.NewReader(updateBody))
	updateReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(updateW, updateReq)
	if updateW.Code != http.StatusOK {
		t.Fatalf("update published post: %d %s", updateW.Code, updateW.Body.String())
	}
	updated := decodePostResponse(t, updateW.Body.Bytes())
	if updated.PublishedAt == nil || !updated.PublishedAt.Equal(firstPublishedAt) {
		t.Fatalf("expected published_at to remain %s, got %#v", firstPublishedAt, updated.PublishedAt)
	}

	listW := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String()+"/versions", nil)
	r.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list versions: %d %s", listW.Code, listW.Body.String())
	}
	var listed struct {
		Data []model.BlogPostVersion `json:"data"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode versions: %v", err)
	}
	if len(listed.Data) != 2 || listed.Data[0].Version != 2 || listed.Data[1].Version != 1 {
		t.Fatalf("expected versions 2 and 1, got %s", listW.Body.String())
	}

	restoreW := httptest.NewRecorder()
	restoreReq := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/versions/1/restore", nil)
	r.ServeHTTP(restoreW, restoreReq)
	if restoreW.Code != http.StatusOK {
		t.Fatalf("restore version: %d %s", restoreW.Code, restoreW.Body.String())
	}
	restored := decodePostResponse(t, restoreW.Body.Bytes())
	if restored.Title != "Version One" || restored.Content != "first body" || restored.PublishedAt == nil || !restored.PublishedAt.Equal(firstPublishedAt) {
		t.Fatalf("unexpected restored post: %#v", restored)
	}
	var versionCount int64
	if err := db.Model(&model.BlogPostVersion{}).Where("post_id = ?", post.ID).Count(&versionCount).Error; err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versionCount != 3 {
		t.Fatalf("expected restore to create version 3, got %d", versionCount)
	}
	if restored.ChannelID == nil || *restored.ChannelID != channel.ID {
		t.Fatalf("expected channel derived from restored collection, got %#v", restored.ChannelID)
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
	collectionID := uuid.New()
	body := `{"context_key":"editor:2","title":"Draft","content":"body","visibility":"followers","allow_comments":false,"collection_id":"` + collectionID.String() + `"}`

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
	if resp.Data.Visibility != "followers" || resp.Data.CollectionID == nil || *resp.Data.CollectionID != collectionID.String() {
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

func TestRegisterRoutesDoNotMountLegacyPostCollectionMutationEndpoints(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, collection := createOwnedChannelAndCollection(t, service, user, "Alice")
	post := createPostRecord(t, db, user.ID, &channel.ID, "Collect me", "draft")
	r := newBlogHTTPRouter(service, &user)

	body, _ := json.Marshal(map[string]string{"collection_id": collection.ID.String()})
	addW := httptest.NewRecorder()
	addReq := httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/"+post.ID.String()+"/collections", bytes.NewReader(body))
	addReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(addW, addReq)
	if addW.Code != http.StatusNotFound {
		t.Fatalf("expected legacy add route to be absent, got %d: %s", addW.Code, addW.Body.String())
	}

	removeW := httptest.NewRecorder()
	removeReq := httptest.NewRequest(http.MethodDelete, "/api/v1/blog/posts/"+post.ID.String()+"/collections/"+collection.ID.String(), nil)
	r.ServeHTTP(removeW, removeReq)
	if removeW.Code != http.StatusNotFound {
		t.Fatalf("expected legacy remove route to be absent, got %d: %s", removeW.Code, removeW.Body.String())
	}
}

func TestRegisterRoutesReorderCollectionPostsPersistsPosition(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, defaultCollection := createOwnedChannelAndCollection(t, service, user, "Alice")
	postA := createPostRecord(t, db, user.ID, &channel.ID, "Post A", "draft")
	postB := createPostRecord(t, db, user.ID, &channel.ID, "Post B", "published")
	postC := createPostRecord(t, db, user.ID, &channel.ID, "Post C", "draft")
	for position, post := range []*model.Post{&postA, &postB, &postC} {
		post.CollectionID = &defaultCollection.ID
		post.CollectionPosition = position
		if err := db.Save(post).Error; err != nil {
			t.Fatalf("assign post to collection: %v", err)
		}
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

	var posts []model.Post
	if err := db.Where("collection_id = ?", defaultCollection.ID).Order("collection_position ASC").Find(&posts).Error; err != nil {
		t.Fatalf("reload positions: %v", err)
	}
	if len(posts) != 3 {
		t.Fatalf("expected 3 posts, got %d", len(posts))
	}
	got := []uuid.UUID{posts[0].ID, posts[1].ID, posts[2].ID}
	want := []uuid.UUID{postC.ID, postA.ID, postB.ID}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected order %v, got %v", want, got)
		}
		if posts[i].CollectionPosition != i {
			t.Fatalf("expected position %d at index %d, got %d", i, i, posts[i].CollectionPosition)
		}
	}
}
