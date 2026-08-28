package blog

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
)

func TestPostRatingCanBeCreatedUpdatedAndDeleted(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	post := model.Post{
		UserID:     user.ID,
		ChannelID:  &channel.ID,
		Title:      "Rated post",
		Content:    "Content",
		Status:     "published",
		Visibility: "public",
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	canonicalizeBlogTestPost(t, db, post)
	r := newBlogHTTPRouter(service, &user)

	requestRating := func(method string, score string) *httptest.ResponseRecorder {
		body := bytes.NewBufferString(score)
		req := httptest.NewRequest(method, "/api/v1/blog/posts/"+post.ID.String()+"/rating", body)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		r.ServeHTTP(response, req)
		return response
	}

	response := requestRating(http.MethodPut, `{"score":7}`)
	if response.Code != http.StatusOK {
		t.Fatalf("create rating status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data PostRatingSummary `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode create rating: %v", err)
	}
	if payload.Data.RatingScore != 7 || payload.Data.RatingCount != 1 || payload.Data.ViewerRating == nil || *payload.Data.ViewerRating != 7 {
		t.Fatalf("unexpected create rating payload: %+v", payload.Data)
	}

	response = requestRating(http.MethodPut, `{"score":10}`)
	if response.Code != http.StatusOK {
		t.Fatalf("update rating status = %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode update rating: %v", err)
	}
	if payload.Data.RatingScore != 10 || payload.Data.RatingCount != 1 || payload.Data.ViewerRating == nil || *payload.Data.ViewerRating != 10 {
		t.Fatalf("unexpected update rating payload: %+v", payload.Data)
	}

	response = requestRating(http.MethodDelete, "")
	if response.Code != http.StatusOK {
		t.Fatalf("delete rating status = %d: %s", response.Code, response.Body.String())
	}
	payload = struct {
		Data PostRatingSummary `json:"data"`
	}{}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode delete rating: %v", err)
	}
	if payload.Data.RatingScore != 0 || payload.Data.RatingCount != 0 || payload.Data.ViewerRating != nil {
		t.Fatalf("unexpected delete rating payload: %+v", payload.Data)
	}
}

func TestPostRatingRejectsScoresOutsideOneToTen(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	post := model.Post{UserID: user.ID, Title: "Rated post", Content: "Content", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	r := newBlogHTTPRouter(service, &user)
	for _, score := range []string{`{"score":0}`, `{"score":11}`} {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/"+post.ID.String()+"/rating", bytes.NewBufferString(score))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		r.ServeHTTP(response, req)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("score %s status = %d: %s", score, response.Code, response.Body.String())
		}
	}

	var count int64
	if err := db.Model(&model.PostRating{}).Where("content_id = ?", post.ID).Count(&count).Error; err != nil {
		t.Fatalf("count ratings: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no invalid ratings, got %d", count)
	}
}

func TestPostRatingConcurrentInitialSubmissionsKeepOneRating(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	post := model.Post{
		UserID:     user.ID,
		ChannelID:  &channel.ID,
		Title:      "Concurrent rating post",
		Content:    "Content",
		Status:     "published",
		Visibility: "public",
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	canonicalizeBlogTestPost(t, db, post)

	const workers = 20
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for score := 1; score <= workers; score++ {
		group.Add(1)
		go func(score int) {
			defer group.Done()
			_, err := service.SetPostRating(user, post.ID, (score-1)%10+1)
			errs <- err
		}(score)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent post rating: %v", err)
		}
	}

	var count int64
	if err := db.Model(&model.PostRating{}).Where("user_id = ? AND content_id = ?", user.ID, post.ID).Count(&count).Error; err != nil {
		t.Fatalf("count post ratings: %v", err)
	}
	if count != 1 {
		t.Fatalf("post rating count = %d, want 1", count)
	}
}

func TestPostListIncludesRatingSummary(t *testing.T) {
	service, db, user := newBlogHTTPTestService(t)
	channel, err := service.CreateDefaultChannelForUser(user.ID, "Alice")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "Rated post", Content: "Content", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	canonicalizeBlogTestPost(t, db, post)
	other := model.User{Username: "bob", Email: "bob@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	if err := db.Create(&model.PostRating{UserID: user.ID, ContentID: post.ID, Score: 8}).Error; err != nil {
		t.Fatalf("create first rating: %v", err)
	}
	if err := db.Create(&model.PostRating{UserID: other.UUID, ContentID: post.ID, Score: 9}).Error; err != nil {
		t.Fatalf("create second rating: %v", err)
	}
	publishedAt := time.Now().UTC()
	run := model.ReputationRun{Status: model.ReputationRunPublished, QualityAlgorithmVersion: "test", ContributionRuleVersion: "test", WeightAlgorithmVersion: "test", StartedAt: publishedAt, PublishedAt: &publishedAt}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create reputation run: %v", err)
	}
	weightedScore := 8.6
	snapshot := model.BlogQualitySnapshot{RunID: run.ID, PostID: post.ID, ValidRatingCount: 6, WeightedScore: &weightedScore, WeightSum: 1.2, QualityEstimate: 70, QualityActive: true, PriorMean: 50, PriorStrength: 5}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatalf("create blog quality snapshot: %v", err)
	}

	r := newBlogHTTPRouter(service, &user)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/"+post.ID.String(), nil)
	response := httptest.NewRecorder()
	r.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("get post status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			RatingScore          float64  `json:"rating_score"`
			RatingCount          int64    `json:"rating_count"`
			ViewerRating         *int     `json:"viewer_rating"`
			WeightedRatingScore  *float64 `json:"weighted_rating_score"`
			WeightedRatingCount  int      `json:"weighted_rating_count"`
			WeightedRatingActive bool     `json:"weighted_rating_active"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode post: %v", err)
	}
	if payload.Data.RatingScore != 8.5 || payload.Data.RatingCount != 2 || payload.Data.ViewerRating == nil || *payload.Data.ViewerRating != 8 {
		t.Fatalf("unexpected post rating summary: %+v", payload.Data)
	}
	if payload.Data.WeightedRatingScore == nil || *payload.Data.WeightedRatingScore != 8.6 || payload.Data.WeightedRatingCount != 6 || !payload.Data.WeightedRatingActive {
		t.Fatalf("unexpected weighted rating summary: %+v", payload.Data)
	}
}
