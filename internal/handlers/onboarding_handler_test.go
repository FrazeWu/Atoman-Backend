package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newOnboardingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.OnboardingFeedRecommendation{})
	return db
}

func TestSetupOnboardingRoutesMountsRecommendationsEndpoint(t *testing.T) {
	r := gin.New()
	SetupOnboardingRoutes(r, newOnboardingTestDB(t))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/feed/onboarding/recommendations", nil))
	if w.Code == http.StatusNotFound {
		t.Fatalf("recommendation endpoint was not mounted: %s", w.Body.String())
	}
}

func TestGetOnboardingFeedRecommendationsReturnsEnabledExternalSourcesInOrder(t *testing.T) {
	db := newOnboardingTestDB(t)
	first := model.FeedSource{SourceType: "external_rss", RssURL: "https://first.example/feed.xml", Hash: uuid.NewString(), Title: "First", Category: "news", HealthStatus: "healthy"}
	second := model.FeedSource{SourceType: "external_rss", RssURL: "https://second.example/feed.xml", Hash: uuid.NewString(), Title: "Second", Category: "blog", HealthStatus: "degraded"}
	internal := model.FeedSource{SourceType: "internal_user", Hash: uuid.NewString(), Title: "Internal"}
	for _, source := range []*model.FeedSource{&first, &second, &internal} {
		if err := db.Create(source).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.OnboardingFeedRecommendation{FeedSourceID: second.ID, Enabled: true, SortOrder: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.OnboardingFeedRecommendation{FeedSourceID: first.ID, Enabled: true, SortOrder: 0}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.OnboardingFeedRecommendation{FeedSourceID: internal.ID, Enabled: true, SortOrder: -1}).Error; err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	r.GET("/api/v1/feed/onboarding/recommendations", GetOnboardingFeedRecommendations(db))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/feed/onboarding/recommendations", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Items []struct {
			Title        string `json:"title"`
			FeedSourceID string `json:"feed_source_id"`
			HealthStatus string `json:"health_status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 2 || payload.Items[0].Title != "First" || payload.Items[1].Title != "Second" {
		t.Fatalf("unexpected items: %s", w.Body.String())
	}
}

func TestAdminOnboardingFeedRecommendationsCRUD(t *testing.T) {
	db := newOnboardingTestDB(t)
	source := model.FeedSource{SourceType: "external_rss", RssURL: "https://admin.example/feed.xml", Hash: uuid.NewString(), Title: "Admin Feed", Category: "podcast", HealthStatus: "healthy"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.POST("/recommendations", CreateAdminOnboardingFeedRecommendation(db))
	r.PATCH("/recommendations/:id", UpdateAdminOnboardingFeedRecommendation(db))
	r.DELETE("/recommendations/:id", DeleteAdminOnboardingFeedRecommendation(db))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/recommendations", bytes.NewBufferString(`{"feed_source_id":"`+source.ID.String()+`","enabled":true,"sort_order":2}`)))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		Item model.OnboardingFeedRecommendation `json:"item"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/recommendations/"+created.Item.ID.String(), bytes.NewBufferString(`{"enabled":false,"sort_order":0}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/recommendations/"+created.Item.ID.String(), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", w.Code, w.Body.String())
	}
}
