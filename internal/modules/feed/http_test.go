package feed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func signedFeedHTTPTokenForTest(t *testing.T, user authctx.CurrentUser) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  user.ID.String(),
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

func TestGetExploreFeedHandlerAllowsAnonymousPublicRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, _, _ := newFeedTestService(t)

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/explore?sort=popular", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected anonymous public read to return 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []TimelineItemDTO `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected public explore items, got body %s", rr.Body.String())
	}
}

func TestGetExploreSourcesHandlerAllowsAnonymousPublicRead(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, _, _ := newFeedTestService(t)

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/explore/sources?page=1&limit=20", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected anonymous source explore to return 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			ID                string `json:"id"`
			Title             string `json:"title"`
			SubscriptionCount int64  `json:"subscription_count"`
			Category          string `json:"category"`
			RecentItems       []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"recent_items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected source explore items, got body %s", rr.Body.String())
	}
	if len(payload.Data[0].RecentItems) == 0 {
		t.Fatalf("expected source explore item previews, got body %s", rr.Body.String())
	}
	if payload.Data[0].RecentItems[0].Title == "" {
		t.Fatalf("expected source explore preview title, got body %s", rr.Body.String())
	}
	if payload.Data[0].Category == "" {
		t.Fatalf("expected source explore category, got body %s", rr.Body.String())
	}
}

func TestGetExploreSourcesHandlerFiltersByCategory(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, db, _ := newFeedTestService(t)

	newsSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://category-http-news.example.com/feed.xml",
		Hash:         "category-http-news-source-hash",
		Title:        "HTTP Category News",
		Category:     "news",
		HealthStatus: "healthy",
	}
	if err := db.Create(&newsSource).Error; err != nil {
		t.Fatalf("create news source: %v", err)
	}
	now := time.Now().UTC()
	if err := db.Create(&model.FeedItem{
		FeedSourceID: newsSource.ID,
		GUID:         "category-http-news-item",
		Title:        "HTTP Category News Item",
		Link:         "https://category-http-news.example.com/item",
		PublishedAt:  now,
		FetchedAt:    now,
	}).Error; err != nil {
		t.Fatalf("create news item: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/explore/sources?page=1&limit=20&category=news", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected category source explore to return 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected category source explore items, got body %s", rr.Body.String())
	}
	for _, row := range payload.Data {
		if row.Category != "news" {
			t.Fatalf("expected only news category rows, got body %s", rr.Body.String())
		}
	}
}

func TestGetSubscribedFeedHandlerAllowsPublicReadByFeedSourceID(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, db, _ := newFeedTestService(t)

	var feedItem model.FeedItem
	if err := db.Where("title = ?", "Feed item").First(&feedItem).Error; err != nil {
		t.Fatalf("find feed item: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/timeline?feed_source_id="+feedItem.FeedSourceID.String(), nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected public source timeline to return 200, got %d with body %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []TimelineItemDTO `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("expected exactly one source timeline item, got %d with body %s", len(payload.Data), rr.Body.String())
	}
	if payload.Data[0].Type != "feed_item" || payload.Data[0].FeedItem == nil {
		t.Fatalf("expected a feed item payload, got %#v", payload.Data[0])
	}
	if payload.Data[0].FeedItem.FeedSourceID != feedItem.FeedSourceID {
		t.Fatalf("expected feed source %s, got %s", feedItem.FeedSourceID, payload.Data[0].FeedItem.FeedSourceID)
	}
}

func TestGetSubscribedFeedHandlerReturnsPostEngagementCounts(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)

	var post model.Post
	if err := db.Where("title = ?", "Post item").First(&post).Error; err != nil {
		t.Fatalf("find subscribed post: %v", err)
	}
	if err := db.Create(&model.Like{UserID: user.ID, TargetType: "post", TargetID: post.ID}).Error; err != nil {
		t.Fatalf("create post like: %v", err)
	}
	if err := db.Create(&model.DiscussionTarget{
		Kind: "blog_post", ResourceID: post.ID, ResourceKey: post.ID.String(), CommentCount: 1, RootCount: 1,
	}).Error; err != nil {
		t.Fatalf("create discussion target: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/timeline?page=1&limit=20", nil)
	req.Header.Set("Authorization", "Bearer "+signedFeedHTTPTokenForTest(t, user))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected subscribed timeline to return 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			Type string `json:"type"`
			Post *struct {
				ID            uuid.UUID `json:"id"`
				Title         string    `json:"title"`
				Status        string    `json:"status"`
				LikesCount    int64     `json:"likes_count"`
				CommentsCount int64     `json:"comments_count"`
			} `json:"post"`
			FeedItem *struct {
				Title string `json:"title"`
				Link  string `json:"link"`
			} `json:"feed_item"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	var foundPost bool
	var foundExternalItem bool
	for _, item := range payload.Data {
		if item.Post != nil && item.Post.ID == post.ID {
			foundPost = true
			if item.Post.Title != post.Title || item.Post.Status != post.Status {
				t.Fatalf("expected original post fields to remain unchanged, got %#v", item.Post)
			}
			if item.Post.LikesCount != 1 || item.Post.CommentsCount != 1 {
				t.Fatalf("expected post engagement 1/1, got %d/%d: %s", item.Post.LikesCount, item.Post.CommentsCount, rr.Body.String())
			}
		}
		if item.Type == "feed_item" && item.FeedItem != nil && item.FeedItem.Title == "Feed item" {
			foundExternalItem = item.FeedItem.Link == "https://example.com/items/1"
		}
	}
	if !foundPost || !foundExternalItem {
		t.Fatalf("expected subscribed post and unchanged external feed item, got %s", rr.Body.String())
	}
}

func TestGetSubscribedBlogFeedRejectsExternalFeedSourceID(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, db, _ := newFeedTestService(t)
	var source model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&source).Error; err != nil {
		t.Fatalf("find external source: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/timeline?content_type=blog&feed_source_id="+source.ID.String(), nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", rr.Code, rr.Body.String())
	}
	var payload struct {
		Data  []TimelineItemDTO `json:"data"`
		Total int64             `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Total != 0 || len(payload.Data) != 0 {
		t.Fatalf("expected blog mode to exclude external feed source, got %s", rr.Body.String())
	}
}

func TestTimelineWriteHandlersRequireAndAcceptRealAuthMiddleware(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)

	var feedItem model.FeedItem
	if err := db.Where("title = ?", "Feed item").First(&feedItem).Error; err != nil {
		t.Fatalf("find feed item: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	body := []byte(`{"feed_item_ids":["` + feedItem.ID.String() + `"]}`)
	unauthReq := httptest.NewRequest(http.MethodPost, "/api/v1/feed/timeline/mark-read", bytes.NewReader(body))
	unauthReq.Header.Set("Content-Type", "application/json")
	unauthRR := httptest.NewRecorder()
	router.ServeHTTP(unauthRR, unauthReq)
	if unauthRR.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated mark-read to return 401, got %d with body %s", unauthRR.Code, unauthRR.Body.String())
	}

	authReq := httptest.NewRequest(http.MethodPost, "/api/v1/feed/timeline/mark-read", bytes.NewReader(body))
	authReq.Header.Set("Content-Type", "application/json")
	authReq.Header.Set("Authorization", "Bearer "+signedFeedHTTPTokenForTest(t, user))
	authRR := httptest.NewRecorder()
	router.ServeHTTP(authRR, authReq)
	if authRR.Code != http.StatusOK {
		t.Fatalf("expected authenticated mark-read to return 200, got %d with body %s", authRR.Code, authRR.Body.String())
	}

	var count int64
	if err := db.Model(&model.FeedItemRead{}).Where("user_id = ? AND feed_item_id = ?", user.ID, feedItem.ID).Count(&count).Error; err != nil {
		t.Fatalf("count feed item read: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected mark-read to persist one read record, got %d", count)
	}
}

func TestReadingListHandlerUsesUnifiedPagedResponse(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)

	var feedItem model.FeedItem
	if err := db.Where("title = ?", "Feed item").First(&feedItem).Error; err != nil {
		t.Fatalf("find feed item: %v", err)
	}
	if err := db.Create(&model.ReadingListItem{UserID: user.ID, TargetType: "feed_item", TargetID: feedItem.ID, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create reading list item: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/reading-list?page=1&limit=20", nil)
	req.Header.Set("Authorization", "Bearer "+signedFeedHTTPTokenForTest(t, user))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected reading-list to return 200, got %d with body %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []model.ReadingListItem `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Meta.Total != 1 || len(payload.Data) != 1 {
		t.Fatalf("expected one unified reading list item, got total=%d len=%d body=%s", payload.Meta.Total, len(payload.Data), rr.Body.String())
	}
}

func TestReadingListHandlerTogglesAndListsInternalPostTargets(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)
	var post model.Post
	if err := db.Where("status = ?", "published").First(&post).Error; err != nil {
		t.Fatalf("find published post: %v", err)
	}
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)
	token := signedFeedHTTPTokenForTest(t, user)

	toggle := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"target_type": "post", "target_id": post.ID})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/reading-list", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	first := toggle()
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"saved":true`) {
		t.Fatalf("expected post to be saved, got %d %s", first.Code, first.Body.String())
	}
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/feed/reading-list?page=1&page_size=20", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listRR := httptest.NewRecorder()
	router.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list reading list: %d %s", listRR.Code, listRR.Body.String())
	}
	var payload struct {
		Data []model.ReadingListItem `json:"data"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(payload.Data) != 1 || payload.Data[0].TargetType != "post" || payload.Data[0].TargetID != post.ID || payload.Data[0].Post == nil || payload.Data[0].Post.ID != post.ID {
		t.Fatalf("expected hydrated post target, got %s", listRR.Body.String())
	}

	second := toggle()
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"saved":false`) {
		t.Fatalf("expected post to be removed, got %d %s", second.Code, second.Body.String())
	}
}

func TestRecordReadEventHandlerPersistsSourceReadEvent(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	body := []byte(`{"source_type":"external_rss","source_id":"source-1","event_type":"original_click"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/events/read", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+signedFeedHTTPTokenForTest(t, user))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected record read event to return 200, got %d with body %s", rr.Code, rr.Body.String())
	}

	var count int64
	if err := db.Model(&model.SourceReadEvent{}).
		Where("source_type = ? AND source_id = ? AND event_type = ?", "external_rss", "source-1", "original_click").
		Count(&count).Error; err != nil {
		t.Fatalf("count source read events: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one source read event, got %d", count)
	}
}

func TestGetSubscribedFeedHandlerUsesOptionalAuthForSourceFilter(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)

	var subscription model.Subscription
	if err := db.Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
		Where("subscriptions.user_id = ? AND feed_sources.source_type = ?", user.ID, "external_rss").
		First(&subscription).Error; err != nil {
		t.Fatalf("find external subscription: %v", err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  user.ID.String(),
		"username": user.Username,
		"role":     user.Role,
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/timeline?source_id="+subscription.ID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []TimelineItemDTO `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("expected one subscribed source item, got %d with body %s", len(payload.Data), rr.Body.String())
	}
	if payload.Data[0].Type != "feed_item" || payload.Data[0].FeedItem == nil || payload.Data[0].FeedItem.Title != "Feed item" {
		t.Fatalf("expected external feed item, got %#v", payload.Data[0])
	}
}

func TestGetSubscribedFeedHandlerParsesUnreadOnlyFilter(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)

	var feedItem model.FeedItem
	if err := db.Where("title = ?", "Feed item").First(&feedItem).Error; err != nil {
		t.Fatalf("find feed item: %v", err)
	}
	if err := db.Create(&model.FeedItemRead{
		UserID:     user.ID,
		FeedItemID: feedItem.ID,
		ReadAt:     time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("mark feed item read: %v", err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  user.ID.String(),
		"username": user.Username,
		"role":     user.Role,
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/timeline?unread_only=true", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []TimelineItemDTO `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, item := range payload.Data {
		if item.Type == "feed_item" && item.FeedItem != nil && item.FeedItem.ID == feedItem.ID {
			t.Fatalf("expected unread_only to exclude read feed item, got body %s", rr.Body.String())
		}
	}
}

func TestGetSubscribedFeedHandlerRejectsInvalidSourceAndGroupIDs(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, _, _ := newFeedTestService(t)

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	for _, rawURL := range []string{
		"/api/v1/feed/timeline?source_id=not-a-uuid",
		"/api/v1/feed/timeline?group_id=not-a-uuid",
	} {
		req := httptest.NewRequest(http.MethodGet, rawURL, nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d with body %s", rawURL, rr.Code, rr.Body.String())
		}
	}
}

func TestQueryFromContextIncludesContentType(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/feed/timeline?content_type=blog", nil)

	query, err := queryFromContext(c)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if query.ContentType != "blog" {
		t.Fatalf("expected blog content type, got %q", query.ContentType)
	}
}

func TestGetSubscribedFeedHandlerRejectsUnsupportedContentType(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, _, _ := newFeedTestService(t)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	for _, contentType := range []string{"video", "bolg"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/timeline?content_type="+contentType, nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for content_type=%s, got %d with body %s", contentType, rr.Code, rr.Body.String())
		}
	}
}

func TestGetSubscribedFeedHandlerParsesSearchQuery(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, _, user := newFeedTestService(t)

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/timeline?q=Feed+item", nil)
	req.Header.Set("Authorization", "Bearer "+signedFeedHTTPTokenForTest(t, user))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []TimelineItemDTO `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected search results for q, got body %s", rr.Body.String())
	}
	for _, item := range payload.Data {
		if item.Type == "feed_item" && item.FeedItem != nil && item.FeedItem.Title == "Feed item" {
			return
		}
	}
	t.Fatalf("expected Feed item result for q, got %#v", payload.Data)
}

func TestGetSubscribedFeedHandlerSearchMatchesFeedItemContentHTML(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)

	var feedItem model.FeedItem
	if err := db.Where("title = ?", "Feed item").First(&feedItem).Error; err != nil {
		t.Fatalf("find feed item: %v", err)
	}
	if err := db.Model(&feedItem).Updates(map[string]any{
		"full_text_html": "<p>longform body phrase for search</p>",
		"summary":        "short summary only",
	}).Error; err != nil {
		t.Fatalf("update feed item body: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/timeline?q=longform+body+phrase", nil)
	req.Header.Set("Authorization", "Bearer "+signedFeedHTTPTokenForTest(t, user))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []TimelineItemDTO `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected search results for full_text_html, got body %s", rr.Body.String())
	}
	for _, item := range payload.Data {
		if item.Type == "feed_item" && item.FeedItem != nil && item.FeedItem.ID == feedItem.ID {
			return
		}
	}
	t.Fatalf("expected full_text_html search to return feed item %s, got %#v", feedItem.ID, payload.Data)
}

func TestFeedRecommendationModeValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, _, _ := newFeedTestService(t)

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	for _, rawURL := range []string{
		"/api/v1/feed/recommend/articles?mode=invalid",
		"/api/v1/feed/recommend/channels?mode=nope",
	} {
		req := httptest.NewRequest(http.MethodGet, rawURL, nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d with body %s", rawURL, rr.Code, rr.Body.String())
		}
	}
}

func TestFeedRecommendationThemesReturnsCategoryThemes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, _, _ := newFeedTestService(t)

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/recommend/themes?category=blog", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected themes endpoint to return 200, got %d with body %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected blog themes, got body %s", rr.Body.String())
	}
	if payload.Data[0].ID == "" || payload.Data[0].Label == "" || payload.Data[0].Description == "" {
		t.Fatalf("expected theme dto fields, got body %s", rr.Body.String())
	}
}

func TestFeedRecommendationArticlesReturnsData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, _ := newFeedTestService(t)

	var post model.Post
	if err := db.Preload("Channel").Where("title = ?", "Post item").First(&post).Error; err != nil {
		t.Fatalf("find post: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/recommend/articles?mode=hot", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected article recommendation to return 200, got %d with body %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Summary     string `json:"summary"`
			ContentType string `json:"content_type"`
			ImageURL    string `json:"image_url"`
			TargetPath  string `json:"target_path"`
			ScoreLabel  string `json:"score_label"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected article recommendations, got body %s", rr.Body.String())
	}
	first := payload.Data[0]
	if first.ID == "" || first.Title == "" || first.TargetPath == "" || first.ScoreLabel == "" || first.ContentType == "" {
		t.Fatalf("expected lightweight article dto fields, got body %s", rr.Body.String())
	}
	foundInternalPost := false
	for _, item := range payload.Data {
		if item.TargetPath == "/posts/post/"+post.ID.String() {
			foundInternalPost = true
			break
		}
	}
	if !foundInternalPost {
		t.Fatalf("expected article target path %s somewhere in result, got body %s", "/posts/post/"+post.ID.String(), rr.Body.String())
	}
	if payload.Meta.Total == 0 {
		t.Fatalf("expected article recommendation meta total, got body %s", rr.Body.String())
	}
}

func TestFeedRecommendationArticlesFiltersByCategoryAndTheme(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, _ := newFeedTestService(t)

	aiSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://ai.example.com/feed.xml",
		Hash:         "recommend-ai-source-hash",
		Title:        "AI Weekly",
		Category:     "blog",
		HealthStatus: "healthy",
	}
	if err := db.Create(&aiSource).Error; err != nil {
		t.Fatalf("create ai source: %v", err)
	}
	newsSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://news.example.com/feed.xml",
		Hash:         "recommend-news-source-hash",
		Title:        "World News",
		Category:     "news",
		HealthStatus: "healthy",
	}
	if err := db.Create(&newsSource).Error; err != nil {
		t.Fatalf("create news source: %v", err)
	}
	now := time.Now().UTC()
	for _, item := range []model.FeedItem{
		{
			FeedSourceID: aiSource.ID,
			GUID:         "recommend-ai-item",
			Title:        "AI model release",
			Summary:      "Large model tools and agents",
			Link:         "https://ai.example.com/model-release",
			PublishedAt:  now,
			FetchedAt:    now,
		},
		{
			FeedSourceID: newsSource.ID,
			GUID:         "recommend-news-item",
			Title:        "Global market update",
			Summary:      "Macroeconomic headline",
			Link:         "https://news.example.com/market-update",
			PublishedAt:  now.Add(-time.Minute),
			FetchedAt:    now.Add(-time.Minute),
		},
	} {
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create feed item: %v", err)
		}
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/recommend/articles?mode=hot&category=blog&theme=ai", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected article recommendation filter to return 200, got %d with body %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			Title       string `json:"title"`
			ContentType string `json:"content_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected filtered article recommendations, got body %s", rr.Body.String())
	}
	for _, item := range payload.Data {
		if item.ContentType != "blog" {
			t.Fatalf("expected only blog article recommendations, got body %s", rr.Body.String())
		}
		if strings.Contains(item.Title, "Global market update") {
			t.Fatalf("expected theme filter to exclude non-ai article, got body %s", rr.Body.String())
		}
	}
}

func TestFeedRecommendationArticlesIncludesExternalFeedItems(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, _ := newFeedTestService(t)

	if err := db.Exec("DELETE FROM posts").Error; err != nil {
		t.Fatalf("delete posts: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/recommend/articles?mode=hot", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected article recommendation to return 200, got %d with body %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			ContentType string `json:"content_type"`
			TargetPath  string `json:"target_path"`
			ScoreLabel  string `json:"score_label"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected external feed items to appear in article recommendations, got body %s", rr.Body.String())
	}
	if payload.Data[0].ID == "" || payload.Data[0].Title == "" || payload.Data[0].TargetPath == "" || payload.Data[0].ScoreLabel == "" || payload.Data[0].ContentType == "" {
		t.Fatalf("expected lightweight recommendation dto fields, got body %s", rr.Body.String())
	}
	if payload.Meta.Total == 0 {
		t.Fatalf("expected article recommendation meta total, got body %s", rr.Body.String())
	}
}

func TestFeedRecommendationChannelsReturnsData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)

	var channel model.Channel
	if err := db.Where("slug = ?", "alice-channel").First(&channel).Error; err != nil {
		t.Fatalf("find channel: %v", err)
	}
	if err := db.Where("source_type = ? AND source_id = ?", "internal_channel", channel.ID).Delete(&model.FeedSource{}).Error; err != nil {
		t.Fatalf("delete channel feed source: %v", err)
	}
	deletedChannel := model.Channel{
		UserID:      &user.ID,
		Name:        "Deleted Channel",
		Slug:        "deleted-channel",
		ContentType: model.ChannelContentTypeBlog,
	}
	if err := db.Create(&deletedChannel).Error; err != nil {
		t.Fatalf("create deleted channel: %v", err)
	}
	deletedPost := model.Post{
		UserID:     user.ID,
		ChannelID:  &deletedChannel.ID,
		Title:      "Deleted channel post",
		Content:    "deleted channel content",
		Status:     "published",
		Visibility: "public",
	}
	if err := db.Create(&deletedPost).Error; err != nil {
		t.Fatalf("create deleted channel post: %v", err)
	}
	if err := db.Delete(&deletedChannel).Error; err != nil {
		t.Fatalf("soft delete channel: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/recommend/channels?mode=featured", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected channel recommendation to return 200, got %d with body %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Summary     string `json:"summary"`
			ContentType string `json:"content_type"`
			ImageURL    string `json:"image_url"`
			TargetPath  string `json:"target_path"`
			ScoreLabel  string `json:"score_label"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected channel recommendations, got body %s", rr.Body.String())
	}
	first := payload.Data[0]
	if first.ID == "" || first.Title == "" || first.TargetPath == "" || first.ScoreLabel == "" || first.ContentType == "" {
		t.Fatalf("expected lightweight channel dto fields, got body %s", rr.Body.String())
	}
	foundInternalChannel := false
	for _, item := range payload.Data {
		if item.TargetPath == "/channels/"+channel.Slug {
			foundInternalChannel = true
		}
		if item.TargetPath == "/channels/"+deletedChannel.Slug {
			t.Fatalf("soft-deleted channel appeared in recommendations: %s", rr.Body.String())
		}
	}
	if !foundInternalChannel {
		t.Fatalf("expected channel target path %s somewhere in result, got body %s", "/channels/"+channel.Slug, rr.Body.String())
	}
	var recreatedSource model.FeedSource
	if err := db.Where("source_type = ? AND source_id = ?", "internal_channel", channel.ID).First(&recreatedSource).Error; err != nil {
		t.Fatalf("expected recommendation to recreate missing channel feed source: %v", err)
	}
	if payload.Meta.Total == 0 {
		t.Fatalf("expected channel recommendation meta total, got body %s", rr.Body.String())
	}
}

func TestFeedRecommendationChannelsIncludePreviewAndStats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)

	var channel model.Channel
	if err := db.Where("slug = ?", "alice-channel").First(&channel).Error; err != nil {
		t.Fatalf("find channel: %v", err)
	}
	if err := db.Model(&channel).Update("description", "关注模型、工具、应用与研究动态").Error; err != nil {
		t.Fatalf("update channel description: %v", err)
	}

	otherUser := model.User{Username: "bob", Email: "bob@example.com", Password: "hash", Role: authctx.RoleUser, DisplayName: "Bob", IsActive: true}
	if err := db.Create(&otherUser).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	var channelSource model.FeedSource
	if err := db.Where("source_type = ? AND source_id = ?", "internal_channel", channel.ID).First(&channelSource).Error; err != nil {
		t.Fatalf("find channel source: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: otherUser.UUID, FeedSourceID: channelSource.ID, Title: channel.Name}).Error; err != nil {
		t.Fatalf("create second channel subscription: %v", err)
	}

	if err := db.Exec("INSERT INTO source_read_events (source_type, source_id, event_type, created_at) VALUES (?, ?, ?, ?), (?, ?, ?, ?)",
		"internal_channel", channel.ID.String(), "detail_open", time.Now().Add(-2*time.Hour).UTC(),
		"internal_channel", channel.ID.String(), "detail_open", time.Now().Add(-1*time.Hour).UTC(),
	).Error; err != nil {
		t.Fatalf("seed channel read events: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/recommend/channels?mode=featured", nil)
	req.Header.Set("Authorization", "Bearer "+signedFeedHTTPTokenForTest(t, user))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected channel recommendation to return 200, got %d with body %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			TargetPath           string `json:"target_path"`
			Description          string `json:"description"`
			UpdateFrequencyLabel string `json:"update_frequency_label"`
			BookmarkCount        int64  `json:"bookmark_count"`
			ReadCount            int64  `json:"read_count"`
			RecentItems          []struct {
				Title string `json:"title"`
			} `json:"recent_items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected channel recommendations, got body %s", rr.Body.String())
	}

	var target *struct {
		TargetPath           string `json:"target_path"`
		Description          string `json:"description"`
		UpdateFrequencyLabel string `json:"update_frequency_label"`
		BookmarkCount        int64  `json:"bookmark_count"`
		ReadCount            int64  `json:"read_count"`
		RecentItems          []struct {
			Title string `json:"title"`
		} `json:"recent_items"`
	}
	for i := range payload.Data {
		if payload.Data[i].TargetPath == "/channels/"+channel.Slug {
			target = &payload.Data[i]
			break
		}
	}
	if target == nil {
		t.Fatalf("expected internal channel recommendation, got body %s", rr.Body.String())
	}
	if target.Description == "" {
		t.Fatalf("expected channel description, got body %s", rr.Body.String())
	}
	if target.UpdateFrequencyLabel == "" {
		t.Fatalf("expected update frequency label, got body %s", rr.Body.String())
	}
	if target.BookmarkCount < 2 {
		t.Fatalf("expected internal channel bookmark count >= 2, got body %s", rr.Body.String())
	}
	if target.ReadCount < 2 {
		t.Fatalf("expected internal channel read count >= 2, got body %s", rr.Body.String())
	}
	if len(target.RecentItems) == 0 || target.RecentItems[0].Title == "" {
		t.Fatalf("expected channel recent items, got body %s", rr.Body.String())
	}
}

func TestFeedRecommendationChannelsFiltersByCategoryAndTheme(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, _ := newFeedTestService(t)

	podcastSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://podcast.example.com/feed.xml",
		Hash:         "recommend-podcast-source-hash",
		Title:        "Startup Podcast",
		Category:     "podcast",
		HealthStatus: "healthy",
	}
	if err := db.Create(&podcastSource).Error; err != nil {
		t.Fatalf("create podcast source: %v", err)
	}
	videoSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://video.example.com/feed.xml",
		Hash:         "recommend-video-source-hash",
		Title:        "Cinema Video",
		Category:     "video",
		HealthStatus: "healthy",
	}
	if err := db.Create(&videoSource).Error; err != nil {
		t.Fatalf("create video source: %v", err)
	}
	now := time.Now().UTC()
	for _, item := range []model.FeedItem{
		{
			FeedSourceID:  podcastSource.ID,
			GUID:          "recommend-podcast-item",
			Title:         "Founder interview",
			Summary:       "Startup operators talk funding",
			Link:          "https://podcast.example.com/founder-interview",
			PublishedAt:   now,
			FetchedAt:     now,
			EnclosureType: "audio/mpeg",
		},
		{
			FeedSourceID:  videoSource.ID,
			GUID:          "recommend-video-item",
			Title:         "Movie trailer",
			Summary:       "Cinema release preview",
			Link:          "https://video.example.com/movie-trailer",
			PublishedAt:   now.Add(-time.Minute),
			FetchedAt:     now.Add(-time.Minute),
			EnclosureType: "video/mp4",
		},
	} {
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create feed item: %v", err)
		}
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/recommend/channels?mode=hot&category=podcast&theme=startup", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected channel recommendation filter to return 200, got %d with body %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			Title       string `json:"title"`
			ContentType string `json:"content_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected filtered channel recommendations, got body %s", rr.Body.String())
	}
	for _, item := range payload.Data {
		if item.ContentType != "podcast" {
			t.Fatalf("expected only podcast channel recommendations, got body %s", rr.Body.String())
		}
		if strings.Contains(item.Title, "Cinema Video") {
			t.Fatalf("expected theme filter to exclude non-startup channel, got body %s", rr.Body.String())
		}
	}
}

func TestFeedRecommendationExternalChannelsIncludePreviewAndStats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)

	var source model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&source).Error; err != nil {
		t.Fatalf("find external source: %v", err)
	}
	now := time.Now().UTC()
	for i, title := range []string{"Newest feed title", "Second feed title", "Third feed title"} {
		item := model.FeedItem{
			FeedSourceID: source.ID,
			GUID:         fmt.Sprintf("preview-guid-%d", i),
			Title:        title,
			Link:         fmt.Sprintf("https://example.com/preview/%d", i),
			PublishedAt:  now.Add(-time.Duration(i) * 24 * time.Hour),
			FetchedAt:    now.Add(-time.Duration(i) * 24 * time.Hour),
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create preview item: %v", err)
		}
	}
	secondUser := model.User{Username: "charlie", Email: "charlie@example.com", Password: "hash", Role: authctx.RoleUser, DisplayName: "Charlie", IsActive: true}
	if err := db.Create(&secondUser).Error; err != nil {
		t.Fatalf("create second user: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: secondUser.UUID, FeedSourceID: source.ID, Title: source.Title}).Error; err != nil {
		t.Fatalf("create second external subscription: %v", err)
	}

	if err := db.Exec("INSERT INTO source_read_events (source_type, source_id, event_type, created_at) VALUES (?, ?, ?, ?), (?, ?, ?, ?), (?, ?, ?, ?)",
		"external_rss", source.ID.String(), "detail_open", now.Add(-3*time.Hour),
		"external_rss", source.ID.String(), "original_click", now.Add(-2*time.Hour),
		"external_rss", source.ID.String(), "original_click", now.Add(-1*time.Hour),
	).Error; err != nil {
		t.Fatalf("seed external source read events: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/recommend/channels?mode=hot", nil)
	req.Header.Set("Authorization", "Bearer "+signedFeedHTTPTokenForTest(t, user))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected external channel recommendation to return 200, got %d with body %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			Title                string `json:"title"`
			Description          string `json:"description"`
			UpdateFrequencyLabel string `json:"update_frequency_label"`
			BookmarkCount        int64  `json:"bookmark_count"`
			ReadCount            int64  `json:"read_count"`
			RecentItems          []struct {
				Title string `json:"title"`
			} `json:"recent_items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected channel recommendations, got body %s", rr.Body.String())
	}

	var target *struct {
		Title                string `json:"title"`
		Description          string `json:"description"`
		UpdateFrequencyLabel string `json:"update_frequency_label"`
		BookmarkCount        int64  `json:"bookmark_count"`
		ReadCount            int64  `json:"read_count"`
		RecentItems          []struct {
			Title string `json:"title"`
		} `json:"recent_items"`
	}
	for i := range payload.Data {
		if payload.Data[i].Title == source.Title {
			target = &payload.Data[i]
			break
		}
	}
	if target == nil {
		t.Fatalf("expected external source recommendation, got body %s", rr.Body.String())
	}
	if target.BookmarkCount < 2 {
		t.Fatalf("expected external source bookmark count >= 2, got body %s", rr.Body.String())
	}
	if target.ReadCount < 3 {
		t.Fatalf("expected external source read count >= 3, got body %s", rr.Body.String())
	}
	if target.UpdateFrequencyLabel == "" {
		t.Fatalf("expected external source update frequency label, got body %s", rr.Body.String())
	}
	if len(target.RecentItems) < 3 {
		t.Fatalf("expected external source recent items, got body %s", rr.Body.String())
	}
}

func TestFeedRecommendationChannelsIncludesExternalSources(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, _ := newFeedTestService(t)

	if err := db.Exec("DELETE FROM posts").Error; err != nil {
		t.Fatalf("delete posts: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/recommend/channels?mode=hot", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected channel recommendation to return 200, got %d with body %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			ContentType string `json:"content_type"`
			TargetPath  string `json:"target_path"`
			ScoreLabel  string `json:"score_label"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected external sources to appear in channel recommendations, got body %s", rr.Body.String())
	}
	if payload.Data[0].ID == "" || payload.Data[0].Title == "" || payload.Data[0].TargetPath == "" || payload.Data[0].ScoreLabel == "" || payload.Data[0].ContentType == "" {
		t.Fatalf("expected lightweight recommendation dto fields, got body %s", rr.Body.String())
	}
	if payload.Meta.Total == 0 {
		t.Fatalf("expected channel recommendation meta total, got body %s", rr.Body.String())
	}
}

func TestMarkUnreadHandlerDeletesReadRecord(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)

	var feedItem model.FeedItem
	if err := db.Where("title = ?", "Feed item").First(&feedItem).Error; err != nil {
		t.Fatalf("find feed item: %v", err)
	}
	if err := db.Create(&model.FeedItemRead{UserID: user.ID, FeedItemID: feedItem.ID, ReadAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create feed item read: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	body := []byte(`{"feed_item_ids":["` + feedItem.ID.String() + `"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/timeline/mark-unread", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+signedFeedHTTPTokenForTest(t, user))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected mark-unread to return 200, got %d with body %s", rr.Code, rr.Body.String())
	}
	var count int64
	if err := db.Model(&model.FeedItemRead{}).Where("user_id = ? AND feed_item_id = ?", user.ID, feedItem.ID).Count(&count).Error; err != nil {
		t.Fatalf("count feed item reads: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected mark-unread to delete read record, got %d", count)
	}
}
