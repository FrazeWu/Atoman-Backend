package debate

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterRoutesMountsWikiAndReadOnlyGraphOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), NewService(testdb.Open(t)))
	routes := map[string]bool{}
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}

	want := []string{
		"GET /api/v1/debate/topics", "POST /api/v1/debate/topics",
		"GET /api/v1/debate/topics/:id", "PUT /api/v1/debate/topics/:id",
		"POST /api/v1/debate/topics/:id/archive",
		"GET /api/v1/debate/topics/:id/revisions",
		"GET /api/v1/debate/topics/:id/revisions/:revisionID",
		"GET /api/v1/debate/topics/:id/revisions/:revisionID/diff",
		"POST /api/v1/debate/topics/:id/revisions/:revisionID/revert",
		"POST /api/v1/debate/topics/:id/references/:relationID/reconfirm",
		"PUT /api/v1/debate/topics/:id/protection", "DELETE /api/v1/debate/topics/:id/protection",
		"GET /api/v1/debates/:id/relations",
	}
	for _, route := range want {
		require.Truef(t, routes[route], "missing route %s", route)
	}
	removed := []string{
		"POST /api/v1/debate/topics/:debateID/arguments",
		"POST /api/v1/debates/:debateID/arguments",
		"PATCH /api/v1/debate-arguments/:argumentID",
		"POST /api/v1/debate-arguments/:argumentID/reference",
		"POST /api/v1/debate-relations",
		"DELETE /api/v1/debate-relations/:relationID",
		"POST /api/v1/debate/topics/:debateID/conclude",
		"POST /api/v1/debate/topics/:debateID/reopen",
	}
	for _, route := range removed {
		require.Falsef(t, routes[route], "legacy route must be removed: %s", route)
	}
}

func TestWikiHTTPValidationAuthorizationAndEnvelopes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := newDebateTestContext(t)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		switch c.GetHeader("X-Test-User") {
		case "editor":
			authctx.SetCurrentUser(c, ctx.editor)
		case "admin":
			authctx.SetCurrentUser(c, ctx.admin)
		}
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1"), ctx.service)
	created := createDebateForTest(t, ctx, "HTTP", "body")

	response := performDebateRequest(t, router, http.MethodPut, "/api/v1/debate/topics/"+created.ID.String(), map[string]any{}, "")
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	response = performDebateRequestRaw(router, http.MethodPut, "/api/v1/debate/topics/"+created.ID.String(), []byte(`{"title":`), "editor")
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	response = performDebateRequest(t, router, http.MethodPut, "/api/v1/debate/topics/not-a-uuid", map[string]any{"title": "Bad", "edit_summary": "bad", "base_revision": created.CurrentRevisionID}, "editor")
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	response = performDebateRequest(t, router, http.MethodGet, "/api/v1/debates/"+created.ID.String()+"/relations?depth=zero", nil, "")
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())

	saved, err := ctx.service.SaveWiki(ctx.editor, created.ID, SaveWikiRequest{Title: "New", Content: "body", EditSummary: "advance", BaseRevisionID: *created.CurrentRevisionID})
	require.NoError(t, err)
	response = performDebateRequest(t, router, http.MethodPut, "/api/v1/debate/topics/"+created.ID.String(), map[string]any{
		"title": "Conflict", "content": "body", "edit_summary": "stale", "base_revision": created.CurrentRevisionID,
	}, "editor")
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	var conflict struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &conflict))
	require.Equal(t, created.CurrentRevisionID.String(), conflict.Error.Details["base_revision_id"])
	require.Equal(t, saved.CurrentRevisionID.String(), conflict.Error.Details["current_revision_id"])

	response = performDebateRequest(t, router, http.MethodPut, "/api/v1/debate/topics/"+created.ID.String(), map[string]any{
		"title": "HTTP saved", "content": "body", "edit_summary": "save", "base_revision": saved.CurrentRevisionID,
	}, "editor")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assertDebateDataEnvelope(t, response)
}

func TestWikiHTTPAdminAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := newDebateTestContext(t)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if c.GetHeader("X-Test-User") == "admin" {
			authctx.SetCurrentUser(c, ctx.admin)
		} else if c.GetHeader("X-Test-User") == "editor" {
			authctx.SetCurrentUser(c, ctx.editor)
		}
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1"), ctx.service)
	protected := createDebateForTest(t, ctx, "Protected", "body")
	archived := createDebateForTest(t, ctx, "Archived", "body")

	response := performDebateRequest(t, router, http.MethodPut, "/api/v1/debate/topics/"+protected.ID.String()+"/protection", map[string]any{"protection_level": "full"}, "editor")
	require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	response = performDebateRequest(t, router, http.MethodPost, "/api/v1/debate/topics/"+archived.ID.String()+"/archive", nil, "editor")
	require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())

	response = performDebateRequest(t, router, http.MethodPut, "/api/v1/debate/topics/"+protected.ID.String()+"/protection", map[string]any{"protection_level": "full"}, "admin")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assertDebateDataEnvelope(t, response)
	response = performDebateRequest(t, router, http.MethodPost, "/api/v1/debate/topics/"+archived.ID.String()+"/archive", nil, "admin")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assertDebateDataEnvelope(t, response)
}

func performDebateRequest(t *testing.T, router *gin.Engine, method, path string, body any, user string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}
	return performDebateRequestRaw(router, method, path, raw, user)
}

func performDebateRequestRaw(router *gin.Engine, method, path string, body []byte, user string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		request.Header.Set("X-Test-User", user)
	}
	router.ServeHTTP(response, request)
	return response
}

func assertDebateDataEnvelope(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.NotEmpty(t, payload["data"], response.Body.String())
}
