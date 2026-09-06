package feed

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
)

func TestSubscriptionHubHandlersExposeTypeScopedTreeAndUpdates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)
	testdb.Migrate(t, db, &model.SubscriptionGroup{})

	var source model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&source).Error; err != nil {
		t.Fatalf("load external source: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)
	token := signedFeedHTTPTokenForTest(t, db, user)

	treeRequest := httptest.NewRequest(http.MethodGet, "/api/v1/feed/subscription-hub/tree", nil)
	treeRequest.Header.Set("Authorization", "Bearer "+token)
	treeRecorder := httptest.NewRecorder()
	router.ServeHTTP(treeRecorder, treeRequest)
	if treeRecorder.Code != http.StatusOK {
		t.Fatalf("tree status=%d body=%s", treeRecorder.Code, treeRecorder.Body.String())
	}
	var treeResponse struct {
		Data SubscriptionHubTree `json:"data"`
	}
	if err := json.Unmarshal(treeRecorder.Body.Bytes(), &treeResponse); err != nil {
		t.Fatalf("decode tree response: %v", err)
	}
	group := firstSubscriptionHubGroup(treeResponse.Data, SubscriptionHubTypeAll)
	foundSource := false
	if group != nil {
		for _, membership := range group.Memberships {
			if membership.FeedSourceID == source.ID {
				foundSource = true
				break
			}
		}
	}
	if !foundSource {
		t.Fatalf("unexpected tree branch: %#v", group)
	}

	updatesRequest := httptest.NewRequest(http.MethodGet, "/api/v1/feed/subscription-hub/updates?type=all&group_id="+group.ID.String(), nil)
	updatesRequest.Header.Set("Authorization", "Bearer "+token)
	updatesRecorder := httptest.NewRecorder()
	router.ServeHTTP(updatesRecorder, updatesRequest)
	if updatesRecorder.Code != http.StatusOK {
		t.Fatalf("updates status=%d body=%s", updatesRecorder.Code, updatesRecorder.Body.String())
	}
	var updatesResponse TimelineListResponseDTO
	if err := json.Unmarshal(updatesRecorder.Body.Bytes(), &updatesResponse); err != nil {
		t.Fatalf("decode updates response: %v", err)
	}
	foundItem := false
	for _, item := range updatesResponse.Data {
		if item.FeedItem != nil && item.FeedItem.FeedSourceID == source.ID {
			foundItem = true
			break
		}
	}
	if !foundItem {
		t.Fatalf("unexpected RSS update stream: %#v", updatesResponse.Data)
	}
}

func TestDeleteSubscriptionHubSourceRemovesAllBackingSubscriptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)
	testdb.Migrate(t, db, &model.SubscriptionGroup{})

	var source model.FeedSource
	if err := db.Where("source_type = ?", "internal_channel").First(&source).Error; err != nil {
		t.Fatalf("load internal channel source: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: user.ID, FeedSourceID: source.ID, Title: source.Title}).Error; err != nil {
		t.Fatalf("create channel subscription: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)
	token := signedFeedHTTPTokenForTest(t, db, user)
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/feed/subscription-hub/sources/"+source.ID.String(), nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	var subscriptionCount int64
	if err := db.Model(&model.Subscription{}).Where("user_id = ? AND feed_source_id = ?", user.ID, source.ID).Count(&subscriptionCount).Error; err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if subscriptionCount != 0 {
		t.Fatalf("subscription remained: %d", subscriptionCount)
	}
}
