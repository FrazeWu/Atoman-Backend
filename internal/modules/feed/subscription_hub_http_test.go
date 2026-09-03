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
	testdb.Migrate(t, db, &model.SubscriptionHubGroup{}, &model.SubscriptionHubMembership{})

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
	group := firstSubscriptionHubGroup(treeResponse.Data, SubscriptionHubTypeRSS)
	if group == nil || len(group.Memberships) != 1 || group.Memberships[0].FeedSourceID != source.ID {
		t.Fatalf("unexpected tree branch: %#v", group)
	}

	updatesRequest := httptest.NewRequest(http.MethodGet, "/api/v1/feed/subscription-hub/updates?type=rss&group_id="+group.ID.String(), nil)
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
	if len(updatesResponse.Data) != 1 || updatesResponse.Data[0].FeedItem == nil || updatesResponse.Data[0].FeedItem.FeedSourceID != source.ID {
		t.Fatalf("unexpected RSS update stream: %#v", updatesResponse.Data)
	}
}

func TestDeleteSubscriptionHubSourceRemovesAllBackingSubscriptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)
	testdb.Migrate(t, db, &model.ChannelBookmark{}, &model.SubscriptionHubGroup{}, &model.SubscriptionHubMembership{})

	var source model.FeedSource
	if err := db.Where("source_type = ?", "internal_channel").First(&source).Error; err != nil {
		t.Fatalf("load internal channel source: %v", err)
	}
	if source.SourceID == nil {
		t.Fatal("internal channel source is missing source_id")
	}
	if err := db.Create(&[]model.ChannelBookmark{
		{UserID: user.ID, ChannelID: *source.SourceID, Kind: "podcast_show"},
		{UserID: user.ID, ChannelID: *source.SourceID, Kind: "video_channel"},
	}).Error; err != nil {
		t.Fatalf("create channel bookmarks: %v", err)
	}
	if _, err := service.GetSubscriptionHubTree(user.ID); err != nil {
		t.Fatalf("seed subscription hub tree: %v", err)
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
	var bookmarkCount int64
	if err := db.Model(&model.ChannelBookmark{}).Where("user_id = ? AND channel_id = ?", user.ID, *source.SourceID).Count(&bookmarkCount).Error; err != nil {
		t.Fatalf("count channel bookmarks: %v", err)
	}
	var membershipCount int64
	if err := db.Model(&model.SubscriptionHubMembership{}).Where("user_id = ? AND feed_source_id = ?", user.ID, source.ID).Count(&membershipCount).Error; err != nil {
		t.Fatalf("count subscription hub memberships: %v", err)
	}
	if subscriptionCount != 0 || bookmarkCount != 0 || membershipCount != 0 {
		t.Fatalf("backing records remained: subscriptions=%d bookmarks=%d memberships=%d", subscriptionCount, bookmarkCount, membershipCount)
	}
}
