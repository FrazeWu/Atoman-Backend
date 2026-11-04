package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/collab"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterV1RoutesMountsAtomanMailUnderAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	r := gin.New()
	RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())
	paths := map[string]bool{}
	for _, route := range r.Routes() {
		paths[route.Method+" "+route.Path] = true
	}
	for _, path := range []string{
		"GET /api/v1/mail/me",
		"GET /api/v1/mail/folders",
		"GET /api/v1/mail/messages",
		"POST /api/v1/mail/activate",
		"POST /api/v1/mail/admin/invites",
	} {
		require.True(t, paths[path], path)
	}
	unauthorized := httptest.NewRecorder()
	r.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/mail/me", nil))
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)
}
