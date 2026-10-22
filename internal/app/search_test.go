package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/collab"
	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/modules/search"
	"atoman/internal/platform/authsession"
	"atoman/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterV1RoutesMountsUnifiedSearchWithoutSwitchingLegacySearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.SiteSetting{}, &model.User{}, &model.Revision{}, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookRating{}, &model.BookList{}, &model.FeedSource{}, &model.FeedItem{})
	testdb.Migrate(t, db, &model.Song{}, &model.Album{}, &model.Artist{}, &model.ArtistAlias{}, &model.SongArtist{}, &model.AlbumArtist{}, &model.MusicSongLyric{}, &model.Playlist{}, &model.PlaylistSong{}, &model.PlaylistBookmark{})
	testdb.Migrate(t, db, &model.Channel{}, &model.ContentEntry{}, &model.ContentBlogExtension{}, &model.ContentBlogTag{}, &model.ContentBlogUserTag{}, &model.ContentCollection{}, &model.ContentCollectionMembership{}, &model.ShortNote{})
	testdb.Migrate(t, db, &model.ForumCategory{}, &model.ForumTopic{}, &model.ForumUserTrust{}, &model.ForumCategoryPermission{}, &model.ForumGroup{}, &model.ForumGroupMember{}, &model.DiscussionTarget{}, &model.CommentEntry{}, &model.Video{}, &model.PodcastEpisode{}, &model.Debate{}, &model.TimelinePerson{}, &model.TimelineEvent{})
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
			require.False(t, envelope.Data.Partial, response.Body.String())
			require.Len(t, envelope.Data.Groups, 20)
		}
	}
}

func TestRegisteredPrivateSearchUsesOnlyBooksAndMusic(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.SiteSetting{}, &model.User{}, &model.AuthSession{}, &model.Revision{}, &model.BookWork{},
		&model.BookList{}, &model.BookListBookmark{}, &model.BookPrivateNote{}, &model.BookLibraryEntry{}, &model.Playlist{}, &model.PlaylistBookmark{})
	user := model.User{Username: "private-search", Email: "private-search@example.test", Password: "hash", IsActive: true, Role: "user"}
	require.NoError(t, db.Create(&user).Error)
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	credentials, err := authsession.New(db).Create(user.UUID, authsession.KindAPI)
	require.NoError(t, err)
	router := gin.New()
	registerSearchRoutes(router.Group("/api/v1"), db)
	for _, path := range []string{"/api/v1/search?scope=mine&q=私人", "/api/v1/search/capabilities?scope=mine"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+credentials.Token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		if path == "/api/v1/search/capabilities?scope=mine" {
			var envelope search.CapabilitiesEnvelope
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
			require.Len(t, envelope.Data.Modules, 2)
			require.Equal(t, "books", envelope.Data.Modules[0].Module)
			require.Len(t, envelope.Data.Modules[0].Types, 4)
			require.Equal(t, "music", envelope.Data.Modules[1].Module)
			require.Len(t, envelope.Data.Modules[1].Types, 2)
		} else {
			var envelope search.SearchEnvelope
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
			require.False(t, envelope.Data.Partial, response.Body.String())
			require.Len(t, envelope.Data.Groups, 6)
		}
	}
}
