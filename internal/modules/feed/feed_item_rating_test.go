package feed

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
)

type feedItemRatingPayload struct {
	RatingScore  float64 `json:"rating_score"`
	RatingCount  int64   `json:"rating_count"`
	ViewerRating *int    `json:"viewer_rating"`
}

type feedItemRatingEnvelope struct {
	Data feedItemRatingPayload `json:"data"`
}

func TestFeedItemRatingPersistsUpdatesAndAppearsOnDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)
	var item model.FeedItem
	if err := db.Where("title = ?", "Feed item").First(&item).Error; err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)
	token := "Bearer " + signedFeedHTTPTokenForTest(t, db, user)

	rate := func(score int) feedItemRatingPayload {
		t.Helper()
		req := httptest.NewRequest(
			http.MethodPut,
			"/api/v1/feed/items/"+item.ID.String()+"/rating",
			bytes.NewBufferString(`{"score":`+strconv.Itoa(score)+`}`),
		)
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("set rating status=%d body=%s", response.Code, response.Body.String())
		}
		var payload feedItemRatingEnvelope
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Data
	}

	first := rate(9)
	if first.RatingScore != 9 || first.RatingCount != 1 || first.ViewerRating == nil || *first.ViewerRating != 9 {
		t.Fatalf("unexpected first rating: %+v", first)
	}
	updated := rate(7)
	if updated.RatingScore != 7 || updated.RatingCount != 1 || updated.ViewerRating == nil || *updated.ViewerRating != 7 {
		t.Fatalf("unexpected updated rating: %+v", updated)
	}

	detailRequest := httptest.NewRequest(http.MethodGet, "/api/v1/feed/items/"+item.ID.String(), nil)
	detailRequest.Header.Set("Authorization", token)
	detailResponse := httptest.NewRecorder()
	router.ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("get detail status=%d body=%s", detailResponse.Code, detailResponse.Body.String())
	}
	var detail struct {
		Data struct {
			Item feedItemRatingPayload `json:"item"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Data.Item.RatingScore != 7 || detail.Data.Item.RatingCount != 1 || detail.Data.Item.ViewerRating == nil || *detail.Data.Item.ViewerRating != 7 {
		t.Fatalf("unexpected detail rating: %+v", detail.Data.Item)
	}

	clearRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/feed/items/"+item.ID.String()+"/rating", nil)
	clearRequest.Header.Set("Authorization", token)
	clearResponse := httptest.NewRecorder()
	router.ServeHTTP(clearResponse, clearRequest)
	if clearResponse.Code != http.StatusOK {
		t.Fatalf("clear rating status=%d body=%s", clearResponse.Code, clearResponse.Body.String())
	}
	var cleared feedItemRatingEnvelope
	if err := json.Unmarshal(clearResponse.Body.Bytes(), &cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.Data.RatingScore != 0 || cleared.Data.RatingCount != 0 || cleared.Data.ViewerRating != nil {
		t.Fatalf("unexpected cleared rating: %+v", cleared.Data)
	}
}
