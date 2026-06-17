package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/collab"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

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

func TestRegisterV1RoutesMountsBlogCreatePost(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
		&model.BlogPostRating{},
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
		"title":          "Router Post",
		"content":        "content",
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

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/blog/posts/00000000-0000-0000-0000-000000000001/collections", bytes.NewBufferString(`{"collection_id":"00000000-0000-0000-0000-000000000001"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || !bytes.Contains(w.Body.Bytes(), []byte("blog.post_not_found")) {
		t.Fatalf("expected mounted blog post collection add route to return blog.post_not_found, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/blog/posts/00000000-0000-0000-0000-000000000001/collections/00000000-0000-0000-0000-000000000001", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || !bytes.Contains(w.Body.Bytes(), []byte("blog.post_not_found")) {
		t.Fatalf("expected mounted blog post collection remove route to return blog.post_not_found, got %d: %s", w.Code, w.Body.String())
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
		&model.ForumReply{},
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
	req = httptest.NewRequest(http.MethodPost, "/api/v1/subscription-groups", bytes.NewBufferString(`{"name":"Tech"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected subscription-groups route to be mounted, got 404: %s", w.Body.String())
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
	req = httptest.NewRequest(http.MethodPost, "/api/v1/forum/topics/"+topic.ID.String()+"/bookmark", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected forum engagement route to be mounted, got 404: %s", w.Body.String())
	}
}

func TestRegisterV1RoutesMountsDebateCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Debate{},
		&model.Argument{},
		&model.DebateVote{},
		&model.VoteHistory{},
		&model.DebateConcludeVote{},
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
}
