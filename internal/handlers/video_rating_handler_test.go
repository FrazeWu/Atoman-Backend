package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type videoRatingPayload struct {
	RatingScore  float64 `json:"rating_score"`
	RatingCount  int64   `json:"rating_count"`
	ViewerRating *int    `json:"viewer_rating"`
}

func TestVideoRatingPersistsAndAppearsOnDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	testdb.Migrate(t, db, &model.PostRating{})
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })

	owner := seedVideoUser(t, db)
	viewer := seedVideoUser(t, db)
	video := seedVideoWithState(t, db, owner.UUID, "published", "public")

	router := gin.New()
	SetupVideoRoutes(router, db, nil)
	viewerToken := "Bearer " + apiAuthTokenForTest(t, db, viewer)

	setRequest := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/videos/"+video.ID.String()+"/rating",
		bytes.NewBufferString(`{"score":9}`),
	)
	setRequest.Header.Set("Authorization", viewerToken)
	setRequest.Header.Set("Content-Type", "application/json")
	setResponse := httptest.NewRecorder()
	router.ServeHTTP(setResponse, setRequest)
	require.Equal(t, http.StatusOK, setResponse.Code, setResponse.Body.String())

	var setRating videoRatingPayload
	require.NoError(t, json.Unmarshal(setResponse.Body.Bytes(), &setRating))
	require.Equal(t, 9.0, setRating.RatingScore)
	require.EqualValues(t, 1, setRating.RatingCount)
	require.NotNil(t, setRating.ViewerRating)
	require.Equal(t, 9, *setRating.ViewerRating)

	detailRequest := httptest.NewRequest(http.MethodGet, "/api/v1/videos/"+video.ID.String(), nil)
	detailRequest.Header.Set("Authorization", viewerToken)
	detailResponse := httptest.NewRecorder()
	router.ServeHTTP(detailResponse, detailRequest)
	require.Equal(t, http.StatusOK, detailResponse.Code, detailResponse.Body.String())

	var detailRating videoRatingPayload
	require.NoError(t, json.Unmarshal(detailResponse.Body.Bytes(), &detailRating))
	require.Equal(t, setRating, detailRating)

	clearRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/videos/"+video.ID.String()+"/rating", nil)
	clearRequest.Header.Set("Authorization", viewerToken)
	clearResponse := httptest.NewRecorder()
	router.ServeHTTP(clearResponse, clearRequest)
	require.Equal(t, http.StatusOK, clearResponse.Code, clearResponse.Body.String())

	var clearRating videoRatingPayload
	require.NoError(t, json.Unmarshal(clearResponse.Body.Bytes(), &clearRating))
	require.Equal(t, 0.0, clearRating.RatingScore)
	require.EqualValues(t, 0, clearRating.RatingCount)
	require.Nil(t, clearRating.ViewerRating)
}
