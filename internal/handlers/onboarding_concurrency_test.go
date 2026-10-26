package handlers

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func seedConcurrentOnboarding(t *testing.T, db *gorm.DB) []model.FeedSource {
	t.Helper()
	sources := make([]model.FeedSource, 6)
	for i := range sources {
		sources[i] = model.FeedSource{
			SourceType: "external_rss", RssURL: fmt.Sprintf("https://limit-%d.example.org/feed", i),
			Hash: uuid.NewString(), Title: fmt.Sprintf("limit %d", i),
		}
		if err := db.Create(&sources[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		if err := db.Create(&model.OnboardingFeedRecommendation{FeedSourceID: sources[i].ID, Enabled: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return sources
}

func assertEnabledOnboardingCount(t *testing.T, db *gorm.DB) {
	t.Helper()
	var count int64
	if err := db.Model(&model.OnboardingFeedRecommendation{}).Where("enabled = ?", true).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("expected exactly 5 enabled recommendations after two concurrent requests, got %d", count)
	}
}

func TestOnboardingConcurrentCreateDoesNotExceedFive(t *testing.T) {
	db := newOnboardingTestDB(t)
	sources := seedConcurrentOnboarding(t, db)
	router := gin.New()
	router.POST("/recommendations", CreateAdminOnboardingFeedRecommendation(db))
	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 4; i < 6; i++ {
		wg.Add(1)
		go func(id uuid.UUID) {
			defer wg.Done()
			<-start
			body := fmt.Sprintf("{\"feed_source_id\":%q,\"enabled\":true}", id.String())
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/recommendations", bytes.NewBufferString(body)))
			statuses <- rec.Code
		}(sources[i].ID)
	}
	close(start)
	wg.Wait()
	close(statuses)
	succeeded, limited := 0, 0
	for code := range statuses {
		switch code {
		case http.StatusCreated:
			succeeded++
		case http.StatusBadRequest:
			limited++
		default:
			t.Fatalf("unexpected create status %d", code)
		}
	}
	if succeeded != 1 || limited != 1 {
		t.Fatalf("concurrent creates returned success=%d limited=%d", succeeded, limited)
	}
	assertEnabledOnboardingCount(t, db)
}

func TestOnboardingConcurrentEnableDoesNotExceedFive(t *testing.T) {
	db := newOnboardingTestDB(t)
	sources := seedConcurrentOnboarding(t, db)
	ids := make([]uuid.UUID, 2)
	for i := 4; i < 6; i++ {
		r := model.OnboardingFeedRecommendation{FeedSourceID: sources[i].ID, Enabled: false}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&r).Update("enabled", false).Error; err != nil {
			t.Fatal(err)
		}
		ids[i-4] = r.ID
	}
	router := gin.New()
	router.PATCH("/recommendations/:id", UpdateAdminOnboardingFeedRecommendation(db))
	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id uuid.UUID) {
			defer wg.Done()
			<-start
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/recommendations/"+id.String(), bytes.NewBufferString("{\"enabled\":true}")))
			statuses <- rec.Code
		}(id)
	}
	close(start)
	wg.Wait()
	close(statuses)
	succeeded, limited := 0, 0
	for code := range statuses {
		switch code {
		case http.StatusOK:
			succeeded++
		case http.StatusBadRequest:
			limited++
		default:
			t.Fatalf("unexpected enable status %d", code)
		}
	}
	if succeeded != 1 || limited != 1 {
		t.Fatalf("concurrent enables returned success=%d limited=%d", succeeded, limited)
	}
	assertEnabledOnboardingCount(t, db)
}
