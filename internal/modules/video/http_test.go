package video

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterRoutesIncludesVideoRatings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router, nil, nil)

	routes := make(map[string]struct{})
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}

	require.Contains(t, routes, "PUT /api/v1/videos/:id/rating")
	require.Contains(t, routes, "DELETE /api/v1/videos/:id/rating")
}
