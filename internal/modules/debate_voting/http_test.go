package debate_voting

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRegisterRoutesMountsCommunityVotesAndNoLegacyVoteRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), NewService(testdb.Open(t)))
	routes := map[string]bool{}
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"GET /api/v1/debate/topics/:id/votes",
		"PUT /api/v1/debate/topics/:id/vote",
		"DELETE /api/v1/debate/topics/:id/vote",
		"GET /api/v1/debate/topics/:id/conclusions",
	} {
		require.Truef(t, routes[route], "missing %s", route)
	}
	for _, route := range []string{
		"POST /api/v1/debates/:debateID/vote",
		"POST /api/v1/debate-arguments/:argumentID/vote",
		"POST /api/v1/debates/:debateID/conclusion-vote",
		"DELETE /api/v1/debates/:debateID/conclusion-vote",
	} {
		require.Falsef(t, routes[route], "legacy route must be removed: %s", route)
	}
}

func TestCommunityVoteHTTPBehavior(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Debate{}, &model.DebateVote{}, &model.DebateConclusionEvent{}, &model.DebateRelation{})
	user := model.User{UUID: uuid.New(), Username: "voter", Email: "voter@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	debate := model.Debate{UserID: user.UUID, Title: "Vote", Status: model.DebateStatusActive}
	require.NoError(t, db.Create(&debate).Error)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if c.GetHeader("X-Test-User") == "voter" {
			authctx.SetCurrentUser(c, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role})
		}
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1"), NewService(db))

	response := performVoteRequest(router, http.MethodGet, "/api/v1/debate/topics/"+debate.ID.String()+"/votes", nil, "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assertVoteDataEnvelope(t, response)
	response = performVoteRequest(router, http.MethodGet, "/api/v1/debate/topics/not-a-uuid/votes", nil, "")
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	response = performVoteRequest(router, http.MethodPut, "/api/v1/debate/topics/"+debate.ID.String()+"/vote", []byte(`{"direction":"yes"}`), "")
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	response = performVoteRequest(router, http.MethodPut, "/api/v1/debate/topics/"+debate.ID.String()+"/vote", []byte(`{"direction":"maybe"}`), "voter")
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	response = performVoteRequest(router, http.MethodPut, "/api/v1/debate/topics/"+debate.ID.String()+"/vote", []byte(`{"direction":"yes"}`), "voter")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assertVoteDataEnvelope(t, response)
	response = performVoteRequest(router, http.MethodDelete, "/api/v1/debate/topics/"+debate.ID.String()+"/vote", nil, "voter")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assertVoteDataEnvelope(t, response)
}

func performVoteRequest(router *gin.Engine, method, path string, body []byte, user string) *httptest.ResponseRecorder {
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

func assertVoteDataEnvelope(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.NotEmpty(t, payload["data"], response.Body.String())
}
