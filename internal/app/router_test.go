package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/collab"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
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
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
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

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/subscriptions", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/feed/timeline?page=1&limit=20", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected feed timeline route to be mounted, got 404: %s", w.Body.String())
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
