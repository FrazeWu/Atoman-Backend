package blog

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func createPublicSearchPost(t *testing.T, db *gorm.DB, userID, channelID uuid.UUID, title, summary, content string) model.Post {
	t.Helper()
	post := model.Post{
		UserID: userID, ChannelID: &channelID, Title: title, Summary: summary, Content: content,
		Status: "published", Visibility: "public",
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post %q: %v", title, err)
	}
	return post
}

func TestBlogSearchRanksTitleMatchesAndReturnsSnippet(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	titleMatch := createPublicSearchPost(t, db, user.ID, channel.ID, "Orchid guide", "", "Unrelated body")
	_ = createPublicSearchPost(t, db, user.ID, channel.ID, "Notes", "", "A detailed orchid body passage for readers")
	privatePost := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Orchid private", Content: "private", Status: "published", Visibility: "private"}
	if err := db.Create(&privatePost).Error; err != nil {
		t.Fatalf("create private post: %v", err)
	}
	draft := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Orchid draft", Content: "draft", Status: "draft", Visibility: "public"}
	if err := db.Create(&draft).Error; err != nil {
		t.Fatalf("create draft post: %v", err)
	}

	response := httptest.NewRecorder()
	newBlogHTTPRouter(service, &user).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/blog/search?q=orchid&page=1&page_size=1&sort=relevance", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected search 200, got %d: %s", response.Code, response.Body.String())
	}
	var firstPage struct {
		Data []struct {
			ID      uuid.UUID `json:"id"`
			Snippet string    `json:"snippet"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if len(firstPage.Data) != 1 || firstPage.Data[0].ID != titleMatch.ID {
		t.Fatalf("expected exact title match first, got %s", response.Body.String())
	}
	if firstPage.Data[0].Snippet == "" {
		t.Fatalf("expected result snippet, got %s", response.Body.String())
	}

	response = httptest.NewRecorder()
	newBlogHTTPRouter(service, &user).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/blog/search?q=orchid&page=2&page_size=1&sort=relevance", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected second page 200, got %d: %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte(privatePost.ID.String())) || bytes.Contains(response.Body.Bytes(), []byte(draft.ID.String())) {
		t.Fatalf("public search exposed a private or draft post: %s", response.Body.String())
	}
}

func TestBlogRecommendationFeedbackHidesAndRestoresArticle(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	post := createPublicSearchPost(t, db, user.ID, channel.ID, "Recommendation target", "", "Body")
	router := newBlogHTTPRouter(service, &user)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/blog/recommendation-feedback", bytes.NewBufferString(`{"content_id":"`+post.ID.String()+`","action":"hide"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected feedback 204, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/blog/recommend/posts?mode=hot&q=Recommendation", nil))
	if response.Code != http.StatusOK || bytes.Contains(response.Body.Bytes(), []byte(post.ID.String())) {
		t.Fatalf("expected hidden article to be excluded, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/blog/recommendation-feedback/"+post.ID.String(), nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected feedback restore 204, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/blog/recommend/posts?mode=hot&q=Recommendation", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(post.ID.String())) {
		t.Fatalf("expected restored article in recommendations, got %d: %s", response.Code, response.Body.String())
	}
}

func TestBlogUpdateRejectsStaleBaseUpdatedAt(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Original", Content: "Body", Status: "draft", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	canonical, err := loadCanonicalBlogContent(db, post.ID)
	if err != nil {
		t.Fatalf("load canonical post: %v", err)
	}
	if err := db.Model(&model.ContentEntry{}).Where("id = ?", post.ID).Updates(map[string]any{
		"title": "Newer title", "updated_at": canonical.UpdatedAt.Add(time.Second),
	}).Error; err != nil {
		t.Fatalf("advance canonical entry: %v", err)
	}

	body := bytes.NewBufferString(`{"title":"Stale title","content":"Stale body","status":"draft","channel_id":"` + channel.ID.String() + `","base_updated_at":"` + canonical.UpdatedAt.UTC().Format(time.RFC3339Nano) + `"}`)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String(), body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	newBlogHTTPRouter(service, &user).ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("expected stale update 409, got %d: %s", response.Code, response.Body.String())
	}
	updated, err := loadCanonicalBlogContent(db, post.ID)
	if err != nil {
		t.Fatalf("reload canonical post: %v", err)
	}
	if updated.Title != "Newer title" {
		t.Fatalf("stale update overwrote current content: %#v", updated)
	}
}

func TestBlogDigestOnlyIncludesRecentSubscribedChannelArticles(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	subscribedChannel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create subscribed channel: %v", err)
	}
	otherChannel := model.Channel{UserID: &user.ID, Name: "Other", Slug: "other-" + uuid.NewString()[:8]}
	if err := db.Create(&otherChannel).Error; err != nil {
		t.Fatalf("create other channel: %v", err)
	}
	source := model.FeedSource{SourceType: "internal_channel", SourceID: &subscribedChannel.ID, Hash: "digest-" + uuid.NewString(), Title: subscribedChannel.Name}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create feed source: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: user.ID, FeedSourceID: source.ID}).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	recent := createPublicSearchPost(t, db, user.ID, subscribedChannel.ID, "Digest article", "A summary", "Body")
	unsubscribed := createPublicSearchPost(t, db, user.ID, otherChannel.ID, "Unsubscribed article", "", "Body")
	old := createPublicSearchPost(t, db, user.ID, subscribedChannel.ID, "Old article", "", "Body")
	weekAgo := time.Now().UTC().Add(-8 * 24 * time.Hour)
	if err := db.Model(&model.Post{}).Where("id = ?", old.ID).Update("published_at", weekAgo).Error; err != nil {
		t.Fatalf("age legacy post: %v", err)
	}
	if err := db.Model(&model.ContentEntry{}).Where("id = ?", old.ID).Update("published_at", weekAgo).Error; err != nil {
		t.Fatalf("age canonical post: %v", err)
	}

	response := httptest.NewRecorder()
	newBlogHTTPRouter(service, &user).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/blog/digest?period=week", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected digest 200, got %d: %s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(recent.ID.String())) || bytes.Contains(response.Body.Bytes(), []byte(unsubscribed.ID.String())) || bytes.Contains(response.Body.Bytes(), []byte(old.ID.String())) {
		t.Fatalf("digest violated subscription or period scope: %s", response.Body.String())
	}
}
