package reference

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestReferenceHTTPSearchAndResolve(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, ids := seedReferenceRegistry(t)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), NewService(db))

	search := httptest.NewRequest(http.MethodGet, "/api/v1/references/search?type=post&q=Blog&limit=10", nil)
	searchResponse := httptest.NewRecorder()
	router.ServeHTTP(searchResponse, search)
	require.Equal(t, http.StatusOK, searchResponse.Code, searchResponse.Body.String())
	require.Contains(t, searchResponse.Body.String(), `"label":"Blog Post"`)

	content := fmt.Sprintf("@alice @post:%s", ids["post"])
	body, _ := json.Marshal(map[string]string{"content": content})
	resolve := httptest.NewRequest(http.MethodPost, "/api/v1/references/resolve", bytes.NewReader(body))
	resolve.Header.Set("Content-Type", "application/json")
	resolveResponse := httptest.NewRecorder()
	router.ServeHTTP(resolveResponse, resolve)
	require.Equal(t, http.StatusOK, resolveResponse.Code, resolveResponse.Body.String())
	require.Contains(t, resolveResponse.Body.String(), `"target_type":"user"`)
	require.Contains(t, resolveResponse.Body.String(), `"target_type":"post"`)
	require.Contains(t, resolveResponse.Body.String(), ids["post"].String())
}

func TestReferenceHTTPSearchSupportsMultipleTypes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := seedReferenceRegistry(t)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), NewService(db))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/references/search?type=post&type=thread&type=post&limit=1", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var body struct {
		Data []Target `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Len(t, body.Data, 2)
	require.Equal(t, []string{"post", "thread"}, []string{body.Data[0].Type, body.Data[1].Type})
	require.Equal(t, "Blog Post", body.Data[0].Label)
	require.Equal(t, "Forum Thread", body.Data[1].Label)
}

func TestReferenceHTTPResolveToleratesIncompleteDraftToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := seedReferenceRegistry(t)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), NewService(db))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/references/resolve", bytes.NewBufferString(`{"content":"@post:typing"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `{"data":[]}`, response.Body.String())
}

func TestReferenceHTTPSearchRejectsUnsupportedType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := seedReferenceRegistry(t)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), NewService(db))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/references/search?type=unknown&q=x", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"code":"reference.unsupported_type"`)
}

func TestReferenceHTTPMultiSearchRejectsUnsupportedType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := seedReferenceRegistry(t)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), NewService(db))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/references/search?type=post&type=unknown&q=x", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"code":"reference.unsupported_type"`)
}
