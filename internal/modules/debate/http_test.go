package debate

import (
	"testing"

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
