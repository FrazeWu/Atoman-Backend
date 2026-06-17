package feed

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

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
