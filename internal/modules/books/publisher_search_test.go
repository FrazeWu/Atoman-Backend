package books

import (
	"atoman/internal/model"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchBookPublishersReturnsOnlyActiveAndEscapesWildcards(t *testing.T) {
	svc, _ := newBookWikiTestService(t)
	for _, p := range []model.BookPublisher{
		{Name: "重庆出版社", LifecycleStatus: model.BookLifecycleStatusActive},
		{Name: "新华出版社", LifecycleStatus: model.BookLifecycleStatusActive},
		{Name: "重庆出版社退役", LifecycleStatus: model.BookLifecycleStatusRetired},
	} {
		require.NoError(t, svc.db.Create(&p).Error)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/publishers", (&Handler{service: svc}).searchBookPublishers)
	req := httptest.NewRequest(http.MethodGet, "/publishers?q=%E9%87%8D%E5%BA%86&limit=10", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	require.Equal(t, http.StatusOK, res.Code)
	var payload struct {
		Data struct {
			Items []model.BookPublisher `json:"items"`
			Total int64                 `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &payload))
	require.Equal(t, int64(1), payload.Data.Total)
	require.Equal(t, "重庆出版社", payload.Data.Items[0].Name)
	// Search patterns supplied by users must be treated literally.
	req = httptest.NewRequest(http.MethodGet, "/publishers?q=%25&limit=10", nil)
	res = httptest.NewRecorder()
	router.ServeHTTP(res, req)
	require.Equal(t, http.StatusOK, res.Code)
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &payload))
	require.Zero(t, payload.Data.Total)
}
