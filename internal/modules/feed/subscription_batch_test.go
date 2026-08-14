package feed

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func performBatchSubscriptionRequest(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestBatchUpdateSubscriptionsIsAtomicAndUpdatesOwnedSubscriptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)
	otherUser := seedFeedTestUser(t, db)

	group := model.SubscriptionGroup{UserID: user.UUID, Name: "Research"}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}

	createSubscription := func(ownerID uuid.UUID, suffix string) model.Subscription {
		source := model.FeedSource{
			SourceType: "external_rss",
			Title:      "Feed " + suffix,
			RssURL:     "https://example.com/" + suffix + ".xml",
			Hash:       buildFeedSourceHash("external_rss", nil, "https://example.com/"+suffix+".xml"),
		}
		if err := db.Create(&source).Error; err != nil {
			t.Fatalf("create source: %v", err)
		}
		subscription := model.Subscription{UserID: ownerID, FeedSourceID: source.ID, Title: source.Title}
		if err := db.Create(&subscription).Error; err != nil {
			t.Fatalf("create subscription: %v", err)
		}
		return subscription
	}

	first := createSubscription(user.UUID, "first")
	second := createSubscription(user.UUID, "second")
	foreign := createSubscription(otherUser.UUID, "foreign")

	router := gin.New()
	router.PUT("/batch-update", withFeedAuth(user.UUID, BatchUpdateSubscriptions(db)))

	invalid := performBatchSubscriptionRequest(t, router, http.MethodPut, "/batch-update", gin.H{
		"ids": []uuid.UUID{first.ID, foreign.ID}, "is_muted": true,
	})
	if invalid.Code != http.StatusNotFound {
		t.Fatalf("expected invalid ownership batch to return 404, got %d: %s", invalid.Code, invalid.Body.String())
	}
	if err := db.First(&first, "id = ?", first.ID).Error; err != nil {
		t.Fatalf("reload first subscription: %v", err)
	}
	if first.IsMuted {
		t.Fatal("expected failed batch to leave owned subscription unchanged")
	}

	valid := performBatchSubscriptionRequest(t, router, http.MethodPut, "/batch-update", gin.H{
		"ids": []uuid.UUID{first.ID, second.ID}, "group_id": group.ID, "is_muted": true, "auto_mark_read": true,
	})
	if valid.Code != http.StatusOK {
		t.Fatalf("expected valid batch to return 200, got %d: %s", valid.Code, valid.Body.String())
	}
	var subscriptions []model.Subscription
	if err := db.Where("id IN ?", []uuid.UUID{first.ID, second.ID}).Find(&subscriptions).Error; err != nil {
		t.Fatalf("reload subscriptions: %v", err)
	}
	for _, subscription := range subscriptions {
		if subscription.SubscriptionGroupID == nil || *subscription.SubscriptionGroupID != group.ID || !subscription.IsMuted || !subscription.AutoMarkRead {
			t.Fatalf("subscription was not batch updated: %#v", subscription)
		}
	}
}

func TestBatchDeleteSubscriptionsDeletesAllSelectedSubscriptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	ids := make([]uuid.UUID, 0, 2)
	for _, suffix := range []string{"delete-first", "delete-second"} {
		source := model.FeedSource{
			SourceType: "external_rss",
			Title:      suffix,
			RssURL:     "https://example.com/" + suffix + ".xml",
			Hash:       buildFeedSourceHash("external_rss", nil, "https://example.com/"+suffix+".xml"),
		}
		if err := db.Create(&source).Error; err != nil {
			t.Fatalf("create source: %v", err)
		}
		subscription := model.Subscription{UserID: user.UUID, FeedSourceID: source.ID, Title: suffix}
		if err := db.Create(&subscription).Error; err != nil {
			t.Fatalf("create subscription: %v", err)
		}
		ids = append(ids, subscription.ID)
	}

	router := gin.New()
	router.POST("/batch-delete", withFeedAuth(user.UUID, BatchDeleteSubscriptions(db)))
	response := performBatchSubscriptionRequest(t, router, http.MethodPost, "/batch-delete", gin.H{"ids": ids})
	if response.Code != http.StatusOK {
		t.Fatalf("expected batch delete to return 200, got %d: %s", response.Code, response.Body.String())
	}
	var count int64
	if err := db.Model(&model.Subscription{}).Where("id IN ?", ids).Count(&count).Error; err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected both subscriptions deleted, got %d remaining", count)
	}
}
