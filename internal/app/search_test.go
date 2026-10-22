package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/collab"
	"atoman/internal/model"
	"atoman/internal/modules/search"
	"atoman/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterV1RoutesMountsUnifiedSearchWithoutSwitchingLegacySearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.SiteSetting{}, &model.User{}, &model.Revision{}, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookRating{}, &model.BookList{})
	testdb.Migrate(t, db, &model.Song{}, &model.Album{}, &model.Artist{}, &model.ArtistAlias{}, &model.SongArtist{}, &model.AlbumArtist{}, &model.MusicSongLyric{}, &model.Playlist{}, &model.PlaylistSong{}, &model.PlaylistBookmark{})
	r := gin.New()
	RegisterV1Routes(r, db, nil, nil, collab.NewUserHub(), collab.NewHub())
	paths := make(map[string]bool)
	for _, route := range r.Routes() {
		if route.Method == http.MethodGet {
			paths[route.Path] = true
		}
	}
	for _, path := range []string{"/api/v1/search", "/api/v1/search/capabilities", "/api/v1/references/search", "/api/v1/music/search"} {
		require.True(t, paths[path], path)
	}
	for path, status := range map[string]int{"/api/v1/search/capabilities": 200, "/api/v1/search": 200, "/api/v1/search?q=测试": 200, "/api/v1/search?scope=mine&q=测试": 401} {
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, status, response.Code, response.Body.String())
		if path == "/api/v1/search?q=测试" {
			var envelope search.SearchEnvelope
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
			require.False(t, envelope.Data.Partial)
			require.Len(t, envelope.Data.Groups, 7)
		}
	}
}
