package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/modules/music"
	"atoman/internal/modules/search"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMusicProviderPublicAPIContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Song{}, &model.Album{}, &model.Artist{}, &model.ArtistAlias{},
		&model.SongArtist{}, &model.AlbumArtist{}, &model.MusicSongLyric{}, &model.Playlist{}, &model.PlaylistSong{}, &model.PlaylistBookmark{})
	user := model.User{UUID: uuid.New(), Username: "music-reader", Email: "reader@example.test", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	for i := range 9 {
		artist := model.Artist{Name: fmt.Sprintf("公开艺术家%02d", i), CreatedBy: &user.UUID}
		album := model.Album{Title: fmt.Sprintf("公开专辑%02d", i), UploadedBy: &user.UUID}
		require.NoError(t, db.Create(&artist).Error)
		require.NoError(t, db.Create(&album).Error)
		song := model.Song{Title: fmt.Sprintf("公开歌曲%02d", i), AlbumID: &album.ID, UploadedBy: &user.UUID,
			SourceKey: "private-object-key", PlaybackKey: "private-playback-key", Lyrics: "完整歌词不应进入统一响应"}
		require.NoError(t, db.Create(&song).Error)
		require.NoError(t, db.Create(&model.SongArtist{SongID: song.ID, ArtistID: artist.ID}).Error)
		require.NoError(t, db.Create(&model.AlbumArtist{AlbumID: album.ID, ArtistID: artist.ID}).Error)
		require.NoError(t, db.Create(&model.Playlist{Name: fmt.Sprintf("公开歌单%02d", i), IsPublic: true, UserID: user.UUID}).Error)
	}
	require.NoError(t, db.Create(&model.Song{Title: "私人秘密", LifecycleStatus: "draft", UploadedBy: &user.UUID}).Error)
	require.NoError(t, db.Create(&model.Playlist{Name: "私人秘密", UserID: user.UUID}).Error)
	registry := search.NewRegistry()
	provider := NewMusicProvider(music.NewService(db))
	require.NoError(t, registry.Register(provider))
	service := search.NewService(registry)
	router := gin.New()
	search.RegisterRoutes(router.Group("/api/v1"), service)
	for _, kind := range []string{"song", "album", "artist", "playlist"} {
		for _, view := range []string{"preview", "grouped", "list"} {
			t.Run(kind+"/"+view, func(t *testing.T) {
				path := "/api/v1/search?q=公开&module=music&type=" + kind + "&view=" + view
				if view == "list" {
					path += "&page_size=2&page=2"
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
				require.Equal(t, 200, response.Code, response.Body.String())
				require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
				var envelope search.SearchEnvelope
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
				require.False(t, envelope.Data.Partial)
				items := envelope.Data.Items
				if view == "list" {
					require.EqualValues(t, 9, envelope.Data.Pagination.Total)
					require.True(t, envelope.Data.Pagination.HasMore)
					require.Len(t, items, 2)
				} else {
					require.Len(t, envelope.Data.Groups, 1)
					group := envelope.Data.Groups[0]
					require.Nil(t, group.Total)
					require.True(t, group.HasMore)
					limit := 6
					if view == "preview" {
						limit = 2
					}
					require.Len(t, group.Items, limit)
					items = group.Items
				}
				for _, hit := range items {
					require.NoError(t, search.ValidateSearchHit(hit))
					require.Equal(t, "/music/"+kind+"/"+hit.ID, hit.Href)
				}
				for _, secret := range []string{"私人秘密", "private-object-key", "private-playback-key", "source_key", "audio_url", "waveform_peaks", "完整歌词不应进入统一响应"} {
					require.NotContains(t, response.Body.String(), secret)
				}
			})
		}
	}
	for _, viewer := range []search.Viewer{{}, {ID: user.UUID}, {ID: uuid.New(), Role: authctx.RoleAdmin}, {ID: user.UUID, Role: authctx.RoleOwner}} {
		for _, kind := range []string{"song", "playlist"} {
			result, err := service.Search(context.Background(), viewer, search.SearchRequest{Query: "私人秘密", Module: "music", Types: []string{kind}, View: search.ViewList})
			require.NoError(t, err)
			require.Zero(t, result.Pagination.Total)
		}
	}
	caps, err := service.Capabilities(context.Background(), search.Viewer{}, search.SearchRequest{Module: "music"})
	require.NoError(t, err)
	require.Len(t, caps.Modules[0].Types, 4)
	require.Empty(t, caps.Modules[0].Filters)
	for _, kind := range caps.Modules[0].Types {
		require.False(t, kind.AllowEmptyQuery)
		require.Equal(t, "relevance", kind.DefaultSort)
	}
	require.Len(t, provider.Capabilities(context.Background(), search.Viewer{ID: user.UUID}, search.ScopeMine).Types, 2)
	_, err = provider.Search(context.Background(), search.Viewer{}, search.ProviderSearchRequest{Scope: search.ScopeMine, Type: "playlist"})
	require.Error(t, err)
	for _, request := range []search.SearchRequest{
		{Module: "music", Types: []string{"song"}, View: search.ViewList},
		{Module: "music", Types: []string{"song"}, Query: "公开", Sort: "rating_desc"},
		{Module: "music", Types: []string{"song"}, Query: "公开", Filters: map[string][]string{"language": {"zh"}}},
	} {
		_, err := service.Search(context.Background(), search.Viewer{}, request)
		require.Error(t, err)
	}
}
