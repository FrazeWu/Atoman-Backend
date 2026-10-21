package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/collab"
	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterV1RoutesMountsUnifiedSearchWithoutSwitchingLegacySearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.SiteSetting{}, &model.User{}, &model.Revision{}, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookRating{}, &model.BookList{})
	r := gin.New()
	RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())
	paths := make(map[string]bool)
	for _, route := range r.Routes() {
		if route.Method == http.MethodGet {
			paths[route.Path] = true
		}
	}
	for _, path := range []string{"/api/v1/search", "/api/v1/search/capabilities", "/api/v1/references/search"} {
		require.True(t, paths[path], path)
	}
	for path, status := range map[string]int{"/api/v1/search/capabilities": 200, "/api/v1/search": 200, "/api/v1/search?q=测试": 200, "/api/v1/search?scope=mine&q=测试": 401} {
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, status, response.Code, response.Body.String())
	}
}
