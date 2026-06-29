package feed

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
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
	if err := db.Create(&model.ReadingListItem{UserID: user.ID, FeedItemID: feedItem.ID, CreatedAt: time.Now().UTC()}).Error; err != nil {
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
