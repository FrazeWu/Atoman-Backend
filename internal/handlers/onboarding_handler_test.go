package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
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

func failOnboardingCount(t *testing.T, db *gorm.DB) {
	t.Helper()
	const callback = "audit:fail_onboarding_count"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if _, isCount := tx.Statement.Dest.(*int64); isCount {
			tx.AddError(errors.New("simulated count failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
}

func TestCreateOnboardingRecommendationReportsCountFailure(t *testing.T) {
	db := newOnboardingTestDB(t)
	source := model.FeedSource{
		SourceType: "external_rss", RssURL: "https://new.example/feed.xml",
		Hash: uuid.NewString(), Title: "new", HealthStatus: "healthy",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	failOnboardingCount(t, db)
	router := gin.New()
	router.POST("/recommendations", CreateAdminOnboardingFeedRecommendation(db))
	result := httptest.NewRecorder()
	body := `{"feed_source_id":"` + source.ID.String() + `","enabled":true}`
	router.ServeHTTP(result, httptest.NewRequest(http.MethodPost, "/recommendations", bytes.NewBufferString(body)))
	if result.Code != http.StatusInternalServerError {
		t.Fatalf("expected HTTP 500 on failed count, got %d: %s", result.Code, result.Body.String())
	}
}

func TestEnableOnboardingRecommendationReportsCountFailure(t *testing.T) {
	db := newOnboardingTestDB(t)
	source := model.FeedSource{
		SourceType: "external_rss", RssURL: "https://disabled.example/feed.xml",
		Hash: uuid.NewString(), Title: "disabled", HealthStatus: "healthy",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	recommendation := model.OnboardingFeedRecommendation{FeedSourceID: source.ID, Enabled: false}
	if err := db.Create(&recommendation).Error; err != nil {
		t.Fatal(err)
	}
	// The model has a database default of enabled=true; explicitly persist false.
	if err := db.Model(&recommendation).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	failOnboardingCount(t, db)
	router := gin.New()
	router.PATCH("/recommendations/:id", UpdateAdminOnboardingFeedRecommendation(db))
	result := httptest.NewRecorder()
	router.ServeHTTP(result, httptest.NewRequest(http.MethodPatch, "/recommendations/"+recommendation.ID.String(), bytes.NewBufferString(`{"enabled":true}`)))
	if result.Code != http.StatusInternalServerError {
		t.Fatalf("expected HTTP 500 on failed count, got %d: %s", result.Code, result.Body.String())
	}
}

func TestCreateDisabledOnboardingRecommendationStaysDisabled(t *testing.T) {
	db := newOnboardingTestDB(t)
	source := model.FeedSource{
		SourceType: "external_rss", RssURL: "https://keep-disabled.example/feed.xml",
		Hash: uuid.NewString(), Title: "Disabled by admin", HealthStatus: "healthy",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/recommendations", CreateAdminOnboardingFeedRecommendation(db))
	result := httptest.NewRecorder()
	body := `{"feed_source_id":"` + source.ID.String() + `","enabled":false}`
	router.ServeHTTP(result, httptest.NewRequest(http.MethodPost, "/recommendations", bytes.NewBufferString(body)))
	if result.Code != http.StatusCreated {
		t.Fatalf("expected HTTP 201, got %d: %s", result.Code, result.Body.String())
	}
	var recommendation model.OnboardingFeedRecommendation
	if err := db.Where("feed_source_id = ?", source.ID).First(&recommendation).Error; err != nil {
		t.Fatal(err)
	}
	if recommendation.Enabled {
		t.Fatal("explicit enabled:false must remain false in the database")
	}
	duplicate := httptest.NewRecorder()
	router.ServeHTTP(duplicate, httptest.NewRequest(http.MethodPost, "/recommendations", bytes.NewBufferString(body)))
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate must report HTTP 409, got %d: %s", duplicate.Code, duplicate.Body.String())
	}
}
