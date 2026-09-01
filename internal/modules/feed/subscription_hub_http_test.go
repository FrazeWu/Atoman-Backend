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
	group := model.SubscriptionHubGroup{
		UserID:           user.ID,
		SubscriptionType: SubscriptionHubTypeRSS,
		Name:             "RSS imports",
	}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create RSS group: %v", err)
	}
	membership := model.SubscriptionHubMembership{
		UserID:           user.ID,
		SubscriptionType: SubscriptionHubTypeRSS,
		GroupID:          group.ID,
		FeedSourceID:     source.ID,
		Title:            source.Title,
	}
	if err := db.Create(&membership).Error; err != nil {
		t.Fatalf("create RSS membership: %v", err)
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
	if branch := treeResponse.Data.Group(SubscriptionHubTypeRSS, group.ID); branch == nil || len(branch.Memberships) != 1 || branch.Memberships[0].ID != membership.ID {
		t.Fatalf("unexpected tree branch: %#v", branch)
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
