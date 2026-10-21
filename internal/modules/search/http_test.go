package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authsession"
	"atoman/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func requestHTTP(router http.Handler, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response
}

func TestSearchHTTPContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := &mockProvider{module: "books", types: []TypeCapability{testType("work")}}
	p.search = func(_ context.Context, _ Viewer, req ProviderSearchRequest) (ProviderSearchResult, error) {
		total := int64(1)
		return ProviderSearchResult{Items: testHits("books", "work", 1), Total: &total}, nil
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Header("Vary", "Origin"); c.Next() })
	RegisterRoutes(r.Group("/api/v1"), NewService(testRegistry(t, p)))
	response := requestHTTP(r, "/api/v1/search?q=三体")
	require.Equal(t, 200, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Contains(t, response.Header().Values("Vary"), "Origin")
	require.Contains(t, response.Header().Values("Vary"), "Cookie, Authorization")
	var body SearchEnvelope
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, ViewGrouped, body.Data.View)
	require.Nil(t, body.Data.Groups[0].Total)
	require.Contains(t, response.Body.String(), `"total":null`)
	response = requestHTTP(r, "/api/v1/search?q=三体&module=books&type=work&view=list")
	require.Equal(t, 200, response.Code)
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.EqualValues(t, 1, body.Data.Pagination.Total)
	response = requestHTTP(r, "/api/v1/search/capabilities?module=books&type=work")
	require.Equal(t, 200, response.Code)
	var caps CapabilitiesEnvelope
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &caps))
	require.Len(t, caps.Data.Modules, 1)
	require.Equal(t, "work", caps.Data.Modules[0].Types[0].Key)
	for _, path := range []string{
		"/api/v1/search?module=unknown&q=x", "/api/v1/search?q=x&filter[unknown]=1", "/api/v1/search?q=x&sort=unknown",
		"/api/v1/search?view=list", "/api/v1/search?user_id=forged&scope=mine", "/api/v1/search/capabilities?q=ignored",
	} {
		response = requestHTTP(r, path)
		require.Equal(t, 400, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"parameter":`)
	}
	for _, path := range []string{"/api/v1/search?scope=mine&q=x", "/api/v1/search/capabilities?scope=mine"} {
		response = requestHTTP(r, path)
		require.Equal(t, 401, response.Code)
	}
	p.search = func(context.Context, Viewer, ProviderSearchRequest) (ProviderSearchResult, error) {
		return ProviderSearchResult{}, errors.New("private SQL detail")
	}
	response = requestHTTP(r, "/api/v1/search?q=三体")
	require.Equal(t, 503, response.Code)
	require.NotContains(t, response.Body.String(), "private SQL")
}

func TestSearchHTTPUnregisteredCapabilitiesAreNotAdvertised(t *testing.T) {
	r := gin.New()
	RegisterRoutes(r.Group("/api/v1"), NewService(NewRegistry()))
	response := requestHTTP(r, "/api/v1/search/capabilities")
	require.Equal(t, 200, response.Code)
	require.JSONEq(t, `{"data":{"scope":"public","modules":[]}}`, response.Body.String())
	response = requestHTTP(r, "/api/v1/search")
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), `"groups":[]`)
	response = requestHTTP(r, "/api/v1/search?q=三体")
	require.Equal(t, 503, response.Code)
}

func TestSearchHTTPAuthenticatesCookieAndAPIToken(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{})
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	users := []model.User{
		{Username: "search-a", Email: "search-a@example.test", Password: "hash", Role: "user", IsActive: true},
		{Username: "search-b", Email: "search-b@example.test", Password: "hash", Role: "moderator", IsActive: true},
	}
	for i := range users {
		require.NoError(t, db.Create(&users[i]).Error)
	}
	p := &mockProvider{module: "books", mine: true, types: []TypeCapability{testType("note")}}
	p.search = func(_ context.Context, viewer Viewer, req ProviderSearchRequest) (ProviderSearchResult, error) {
		require.Equal(t, ScopeMine, req.Scope)
		return ProviderSearchResult{Items: []SearchHit{{Module: "books", Type: "note", ID: viewer.ID.String(), Title: viewer.Username, Href: "/books/annotations"}}}, nil
	}
	r := gin.New()
	RegisterRoutes(r.Group("/api/v1"), NewService(testRegistry(t, p)))
	for i, kind := range []string{authsession.KindWeb, authsession.KindAPI} {
		credentials, err := authsession.New(db).Create(users[i].UUID, kind)
		require.NoError(t, err)
		for _, path := range []string{"/api/v1/search?scope=mine&q=笔记", "/api/v1/search/capabilities?scope=mine"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if kind == authsession.KindWeb {
				req.AddCookie(&http.Cookie{Name: middleware.AuthSessionCookieName, Value: credentials.Token})
			} else {
				req.Header.Set("Authorization", "Bearer "+credentials.Token)
			}
			response := httptest.NewRecorder()
			r.ServeHTTP(response, req)
			require.Equal(t, 200, response.Code, response.Body.String())
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			if path == "/api/v1/search?scope=mine&q=笔记" {
				require.Contains(t, response.Body.String(), users[i].UUID.String())
				require.NotContains(t, response.Body.String(), users[1-i].UUID.String())
			}
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?scope=mine&q=笔记", nil)
	req.Header.Set("Authorization", "Bearer invalid")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, req)
	require.Equal(t, 401, response.Code)
}
