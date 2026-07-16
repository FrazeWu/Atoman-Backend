package app

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/collab"
	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func signedRouterTokenForTest(t *testing.T, user model.User) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  user.UUID.String(),
		"username": user.Username,
		"role":     user.Role,
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func TestRegisterV1RoutesMountsMusicSubmitEdit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Artist{},
		&model.Album{},
		&model.Song{},
		&model.MusicEdit{},
		&model.MusicEditVote{},
		&model.MusicEditDecision{},
		&model.MusicEditChange{},
		&model.AuditLog{},
	)
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser})
		c.Next()
	})
	RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())

	body := map[string]any{
		"type":        "create_artist",
		"entity_type": "artist",
		"payload": map[string]any{
			"name": "Router Artist",
		},
		"changes": map[string]any{},
		"reason":  "new artist",
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/edits", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterV1RoutesMountsUnifiedCommentHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	r := gin.New()
	RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/discussions/unknown/"+uuid.NewString()+"/comments", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected mounted unified comment route to return 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"comment.invalid_target"`) {
		t.Fatalf("expected stable comment error, got %s", w.Body.String())
	}
}

func TestRegisterV1RoutesMusicBookmarksAcceptBearerAuthWithoutExplicitRouteMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")

	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Artist{},
		&model.ArtistBookmark{},
	)
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	artist := model.Artist{Name: "Bookmarked Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if err := db.Create(&model.ArtistBookmark{UserID: user.UUID, ArtistID: artist.ID}).Error; err != nil {
		t.Fatalf("create artist bookmark: %v", err)
	}

	r := gin.New()
	RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/bookmarks/artists", nil)
	req.Header.Set("Authorization", "Bearer "+signedRouterTokenForTest(t, user))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			ArtistID string `json:"artist_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ArtistID != artist.ID.String() {
		t.Fatalf("unexpected bookmark payload: %s", w.Body.String())
	}
}

func TestRegisterV1RoutesMountsS3OnlyUploads(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.MediaAsset{})
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("purpose", "music.cover"); err != nil {
		t.Fatalf("write purpose: %v", err)
	}
	part, err := writer.CreateFormFile("file", "avatar.png")
	if err != nil {
		t.Fatalf("create file field: %v", err)
	}
	if _, err := part.Write([]byte("png-data")); err != nil {
		t.Fatalf("write file field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	r := gin.New()
	RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", body)
	req.Header.Set("Authorization", "Bearer "+signedRouterTokenForTest(t, user))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected uploads route to be mounted, got 404")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected mounted uploads route to return 503 without S3, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != `{"code":"storage.unavailable","error":"Storage service is unavailable"}` {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestRegisterV1RoutesMountsBlogCreatePost(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
		&model.BlogDraft{},
		&model.AuditLog{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
	)
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", DisplayName: "Alice", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	channel := model.Channel{UserID: &user.UUID, Name: "Alice", Slug: "alice", IsDefault: true}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	collection := model.Collection{ChannelID: channel.ID, Name: "默认专栏", IsDefault: true}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatalf("create default collection: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser})
		c.Next()
	})
	RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())

	body := map[string]any{
		"title":         "Router Post",
		"content":       "content",
		"channel_id":    channel.ID,
		"collection_id": collection.ID.String(),
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

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected blog posts list route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/00000000-0000-0000-0000-000000000001", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || !bytes.Contains(w.Body.Bytes(), []byte("Post not found")) {
		t.Fatalf("expected mounted detail route to return post not found, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/00000000-0000-0000-0000-000000000001", bytes.NewBufferString(`{"title":"After","content":"After body","summary":"Summary","status":"published"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || !bytes.Contains(w.Body.Bytes(), []byte("blog.post_not_found")) {
		t.Fatalf("expected mounted update route to return blog.post_not_found, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/blog/posts/00000000-0000-0000-0000-000000000001", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || !bytes.Contains(w.Body.Bytes(), []byte("blog.post_not_found")) {
		t.Fatalf("expected mounted delete route to return blog.post_not_found, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/drafts", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected blog drafts list route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/blog/drafts?context_key=test", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || !bytes.Contains(w.Body.Bytes(), []byte("blog.draft_not_found")) {
		t.Fatalf("expected mounted blog draft get route to return blog.draft_not_found, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/blog/drafts", bytes.NewBufferString(`{"context_key":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected mounted blog draft put route to save draft, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/blog/drafts?context_key=test", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected mounted blog draft delete route to delete draft, got %d: %s", w.Code, w.Body.String())
	}

	for _, path := range []string{
		"/api/v1/blog/posts/00000000-0000-0000-0000-000000000001/publish",
		"/api/v1/blog/posts/00000000-0000-0000-0000-000000000001/unpublish",
		"/api/v1/blog/posts/00000000-0000-0000-0000-000000000001/pin",
		"/api/v1/blog/posts/00000000-0000-0000-0000-000000000001/unpin",
	} {
		w = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, path, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound || !bytes.Contains(w.Body.Bytes(), []byte("blog.post_not_found")) {
			t.Fatalf("expected mounted blog post action route %s to return blog.post_not_found, got %d: %s", path, w.Code, w.Body.String())
		}
	}

}

func TestRegisterV1RoutesMountsSiteResolve(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{})

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	channel := model.Channel{UserID: &user.UUID, Name: "Design", Slug: "design"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	r := gin.New()
	RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())

	cases := []struct {
		path     string
		wantCode int
		wantType string
	}{
		{path: "/api/v1/site/resolve/feed", wantCode: http.StatusOK, wantType: "module"},
		{path: "/api/v1/site/resolve/alice", wantCode: http.StatusOK, wantType: "user"},
		{path: "/api/v1/site/resolve/design", wantCode: http.StatusOK, wantType: "channel"},
		{path: "/api/v1/site/resolve/missing", wantCode: http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.ServeHTTP(w, req)
			if w.Code != tc.wantCode {
				t.Fatalf("expected %d, got %d: %s", tc.wantCode, w.Code, w.Body.String())
			}
			if tc.wantType == "" {
				return
			}
			var payload struct {
				Data struct {
					Type string `json:"type"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if payload.Data.Type != tc.wantType {
				t.Fatalf("expected type %q, got %q", tc.wantType, payload.Data.Type)
			}
		})
	}
}

func TestRegisterV1RoutesMountsSubscribedFeed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.FeedSource{},
		&model.Subscription{},
		&model.SubscriptionGroup{},
		&model.FeedItem{},
		&model.FeedItemRead{},
		&model.FeedItemStar{},
		&model.ReadingListItem{},
		&model.Notification{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumDraft{},
		&model.ForumLike{},
		&model.ForumBookmark{},
		&model.ForumReport{},
		&model.CategoryRequest{},
	)
	t.Setenv("JWT_SECRET", "test-secret")
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  user.UUID.String(),
		"username": user.Username,
		"role":     user.Role,
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	category := model.ForumCategory{Name: "General", Description: "General discussion", Color: "#000000"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create forum category: %v", err)
	}
	topic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "Hello", Content: "World"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create forum topic: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleModerator})
		c.Next()
	})
	RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())

	group := model.SubscriptionGroup{UserID: user.UUID, Name: "默认分组"}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create subscription group: %v", err)
	}
	source := model.FeedSource{SourceType: "external_rss", Provider: "rss", RssURL: "https://example.com/feed.xml", CanonicalURL: "https://example.com/feed.xml", Hash: "router-subscriptions-feed-source", Title: "Example Feed"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create feed source: %v", err)
	}
	subscription := model.Subscription{UserID: user.UUID, FeedSourceID: source.ID, Title: "Example Subscription", SubscriptionGroupID: &group.ID}
	if err := db.Create(&subscription).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/subscriptions", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected route to be mounted, got 404: %s", w.Body.String())
	}
	var feedSubscriptions struct {
		Data []struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			FeedSource *struct {
				Title string `json:"title"`
			} `json:"feed_source"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &feedSubscriptions); err != nil {
		t.Fatalf("decode subscriptions response: %v", err)
	}
	if len(feedSubscriptions.Data) != 1 {
		t.Fatalf("expected 1 subscription, got %d with body %s", len(feedSubscriptions.Data), w.Body.String())
	}
	if feedSubscriptions.Data[0].ID != subscription.ID.String() {
		t.Fatalf("expected subscription id %s, got body %s", subscription.ID.String(), w.Body.String())
	}
	if feedSubscriptions.Data[0].FeedSource == nil || feedSubscriptions.Data[0].FeedSource.Title != "Example Feed" {
		t.Fatalf("expected feed_source title Example Feed, got body %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions", bytes.NewBufferString(`{"target_type":"external_rss","rss_url":"https://example.com/feed.xml"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected feed subscription POST route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/feed/timeline?page=1&limit=20", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected feed timeline route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/feed/reading-list?page=1&limit=20", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected feed reading-list GET route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/feed/discover", bytes.NewBufferString(`{"url":"https://example.com/feed.xml"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected feed discover route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/feed/groups", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected feed groups route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/feed/stars?page=1&limit=20", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected feed stars route to be mounted, got 404: %s", w.Body.String())
	}

	item := model.FeedItem{
		FeedSourceID: source.ID,
		GUID:         "router-feed-item",
		Title:        "Router Feed Item",
		Link:         "https://example.com/item",
		PublishedAt:  time.Now().UTC(),
		FetchedAt:    time.Now().UTC(),
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create feed item: %v", err)
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/feed/items/"+item.ID.String(), nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected feed item detail route to return 200, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/feed/reading-list/00000000-0000-0000-0000-000000000001", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected missing reading-list item to return 404, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/notifications/unread-count", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected notification route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions", bytes.NewBufferString(`{"target_type":"external_rss","rss_url":"https://example.com/feed.xml"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected /api/v1/subscriptions to be unmounted, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/subscription-groups", bytes.NewBufferString(`{"name":"Tech"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected /api/v1/subscription-groups to be unmounted, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/forum/moderation/reports", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected forum moderation route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/forum/topics", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected forum core route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/forum/search?q=hello", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected forum search route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/forum/topics/"+topic.ID.String()+"/bookmark", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected forum engagement route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/forum/replies/"+uuid.NewString()+"/like", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected legacy forum reply like route to be removed, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/forum/topics/"+topic.ID.String()+"/close", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected forum close route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/forum/topics/"+topic.ID.String()+"/feature", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected forum feature route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/forum/topics/"+topic.ID.String()+"/feature", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected forum unfeature route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/forum/replies/"+uuid.NewString()+"/solve", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected legacy forum solve route to be removed, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/forum/replies/"+uuid.NewString()+"/solve", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected legacy forum unsolve route to be removed, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/forum/category-requests", bytes.NewBufferString(`{"name":"Suggestions","description":"desc","reason":"please"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected forum category request create route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/forum/category-requests", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected forum category request list route to be mounted, got 404: %s", w.Body.String())
	}

	request := model.CategoryRequest{UserID: user.UUID, Name: "Pending", Description: "desc", Reason: "need", Status: "pending"}
	if err := db.Create(&request).Error; err != nil {
		t.Fatalf("create category request: %v", err)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/forum/category-requests/"+request.ID.String()+"/review", bytes.NewBufferString(`{"action":"reject"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected forum category request review route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/forum/report", bytes.NewBufferString(`{"target_type":"topic","target_id":"`+topic.ID.String()+`","reason":"spam"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected forum report route to be mounted, got 404: %s", w.Body.String())
	}
}

func TestRegisterV1RoutesMountsDebateCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Debate{},
		&model.DebateVote{},
		&model.VoteHistory{},
		&model.DebateConcludeVote{},
		&model.DiscussionTarget{},
		&model.CommentEntry{},
		&model.DebateArgumentDetail{},
	)
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser})
		c.Next()
	})
	RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())

	body := map[string]any{
		"title":       "Router Debate",
		"description": "Body",
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/debates", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode debate create response: %v", err)
	}
	if created.Data.ID == "" {
		t.Fatalf("expected created debate id, got body %s", w.Body.String())
	}

	for _, path := range []string{
		"/api/v1/debate/topics",
		"/api/v1/debate/topics/" + created.Data.ID,
		"/api/v1/debate/topics/search?q=Router",
	} {
		w = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("expected debate route %s to be mounted, got 404: %s", path, w.Body.String())
		}
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/debate/arguments/"+uuid.NewString()+"/vote", bytes.NewBufferString(`{"vote_type":1}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected legacy debate vote route to be unmounted, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/debate/topics/"+uuid.NewString()+"/conclude-vote", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected legacy debate conclude vote route to be unmounted, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/debate-arguments/"+uuid.NewString()+"/vote", bytes.NewBufferString(`{"vote_type":1}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound && !bytes.Contains(w.Body.Bytes(), []byte("debate.argument_not_found")) {
		t.Fatalf("expected module debate argument vote route to be mounted, got router 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/debates/"+uuid.NewString()+"/conclusion-vote", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound && !bytes.Contains(w.Body.Bytes(), []byte("debate.not_found")) {
		t.Fatalf("expected module debate conclusion vote route to be mounted, got router 404: %s", w.Body.String())
	}
}

func TestRegisterV1RoutesDoesNotMountLegacyInteractionRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterV1Routes(router, testdb.Open(t), nil, nil, collab.NewUserHub(), collab.NewHub())

	id := uuid.NewString()
	requests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/blog/posts/" + id + "/comments"},
		{http.MethodPost, "/api/v1/blog/posts/" + id + "/comments"},
		{http.MethodGet, "/api/v1/videos/" + id + "/comments"},
		{http.MethodPost, "/api/v1/videos/" + id + "/comments"},
		{http.MethodGet, "/api/v1/albums/" + id + "/discussions"},
		{http.MethodPost, "/api/v1/albums/" + id + "/discussions"},
		{http.MethodGet, "/api/v1/forum/topics/" + id + "/replies"},
		{http.MethodPost, "/api/v1/forum/replies"},
		{http.MethodPut, "/api/v1/forum/replies/" + id},
		{http.MethodDelete, "/api/v1/forum/replies/" + id},
		{http.MethodPost, "/api/v1/forum/replies/" + id + "/like"},
		{http.MethodPost, "/api/v1/forum/replies/" + id + "/solve"},
		{http.MethodDelete, "/api/v1/forum/replies/" + id + "/solve"},
		{http.MethodGet, "/api/v1/debate/topics/" + id + "/arguments"},
		{http.MethodPost, "/api/v1/debate/topics/" + id + "/arguments"},
		{http.MethodPut, "/api/v1/debate/arguments/" + id},
		{http.MethodDelete, "/api/v1/debate/arguments/" + id},
	}
	for _, request := range requests {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(request.method, request.path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("legacy route %s %s returned %d: %s", request.method, request.path, response.Code, response.Body.String())
		}
	}
}

func TestRegisterV1RoutesOnlyExposeApprovedNonAPIRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	r := gin.New()
	RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())

	allowedPrefixes := []string{"/api/", "/uploads", "/swagger/", "/ws/user"}
	for _, route := range r.Routes() {
		allowed := false
		for _, prefix := range allowedPrefixes {
			if strings.HasPrefix(route.Path, prefix) {
				allowed = true
				break
			}
		}
		if !allowed {
			t.Fatalf("route %s %s is outside /api and not in the explicit non-API allowlist", route.Method, route.Path)
		}
	}
}
