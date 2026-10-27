package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/collab"
	"atoman/internal/middleware"
	"atoman/internal/migrationrunner"
	"atoman/internal/model"
	"atoman/internal/modules/books"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPublicBookRatingResolvesOptionalViewerAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	require.NoError(t, migrationrunner.Run(db))
	user := model.User{Username: "book-rating-reader", Email: "book-rating-reader@example.test", Password: "not-a-login-credential", IsActive: true, Role: "user"}
	other := model.User{Username: "book-rating-other", Email: "book-rating-other@example.test", Password: "not-a-login-credential", IsActive: true, Role: "user"}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&other).Error)
	actor := authctx.CurrentUser{ID: user.UUID, Role: user.Role}
	workID := createRatingAcceptanceBook(t, db, actor)
	_, err := books.NewService(db).SetBookWorkRating(context.Background(), actor, workID, 8)
	require.NoError(t, err)
	apiToken := signedRouterTokenForTest(t, db, user)
	otherToken := signedRouterTokenForTest(t, db, other)
	webSession, err := authsession.New(db).Create(user.UUID, authsession.KindWeb)
	require.NoError(t, err)
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	router := gin.New()
	RegisterV1Routes(router, db, nil, nil, collab.NewUserHub(), collab.NewHub())
	for _, tc := range []struct {
		name, bearer, cookie string
		mine                 bool
	}{
		{name: "anonymous"},
		{name: "API owner", bearer: apiToken, mine: true},
		{name: "web owner", cookie: webSession.Token, mine: true},
		{name: "other account", bearer: otherToken},
		{name: "invalid bearer must not fall back to cookie", bearer: "invalid-token", cookie: webSession.Token},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/books/works/"+workID.String()+"/rating", nil)
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: middleware.AuthSessionCookieName, Value: tc.cookie})
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
			var body struct {
				Data books.BookWorkRatingDTO `json:"data"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			require.EqualValues(t, 1, body.Data.RatingCount)
			if tc.mine {
				require.NotNil(t, body.Data.ViewerRating)
				require.Equal(t, 8, *body.Data.ViewerRating)
			} else {
				require.Nil(t, body.Data.ViewerRating)
			}
		})
	}
}
