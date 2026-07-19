package debate_voting

import (
	"testing"

	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
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
