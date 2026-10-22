package music

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSearchCatalogMatchesLyricsAliasesAndRelationsWithoutDuplicates(t *testing.T) {
	s, db, user := newMusicTestService(t)
	artist := model.Artist{Name: "正式名称", LegalName: "真实姓名", Disambiguation: "消歧信息"}
	require.NoError(t, db.Create(&artist).Error)
	for _, alias := range []string{"别名", "别名二", "别名三", "无关称呼"} {
		require.NoError(t, db.Create(&model.ArtistAlias{ArtistID: artist.ID, Alias: alias}).Error)
	}
	album := model.Album{Title: "专辑名称"}
	require.NoError(t, db.Create(&album).Error)
	for _, role := range []string{"primary", "producer"} {
		require.NoError(t, db.Create(&model.AlbumArtist{AlbumID: album.ID, ArtistID: artist.ID, Role: role}).Error)
	}
	song := model.Song{Title: "歌曲名称", AlbumID: &album.ID, AudioURL: "/uploads/audio.mp3", Lyrics: "旧响应歌词"}
	require.NoError(t, db.Create(&song).Error)
	for _, role := range []string{"primary", "producer"} {
		require.NoError(t, db.Create(&model.SongArtist{SongID: song.ID, ArtistID: artist.ID, Role: role}).Error)
	}
	require.NoError(t, db.Create(&model.MusicSongLyric{SongID: song.ID, Content: "原文词句", Translation: "翻译词句", UpdatedBy: user.ID}).Error)
	require.NoError(t, db.Create(&model.Playlist{Name: "歌单名称", UserID: user.ID, IsPublic: true}).Error)
	for query, totals := range map[string]map[string]int64{
		"歌曲名称": {"song": 1}, "专辑名称": {"song": 1, "album": 1}, "正式名称": {"song": 1, "album": 1, "artist": 1},
		"别名": {"song": 1, "album": 1, "artist": 1}, "原文词句": {"song": 1}, "翻译词句": {"song": 1},
		"真实姓名": {"artist": 1}, "消歧信息": {"artist": 1}, "歌单名称": {"playlist": 1},
	} {
		t.Run(query, func(t *testing.T) {
			got, err := s.SearchCatalog(context.Background(), nil, MusicSearchOptions{Query: query, ExactTotal: true})
			require.NoError(t, err)
			for _, kind := range []string{"song", "album", "artist", "playlist"} {
				require.Equal(t, totals[kind], got.Totals[kind], kind)
			}
			require.Len(t, got.Songs, int(totals["song"]))
			require.Len(t, got.Albums, int(totals["album"]))
			require.Len(t, got.Artists, int(totals["artist"]))
		})
	}
	// 旧 HTTP 响应使用同一查询，保留完整/summary 两种形状。
	for _, view := range []string{"", "summary"} {
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Request = httptest.NewRequest("GET", "/?q="+url.QueryEscape("别名")+"&view="+view, nil)
		(&Handler{service: s}).search(c)
		require.Equal(t, 200, response.Code, response.Body.String())
		var envelope struct {
			Data musicSearchResponse `json:"data"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
		require.Len(t, envelope.Data.Songs, 1)
		require.EqualValues(t, 1, envelope.Data.Meta.Totals["song"])
		if view == "" {
			require.Contains(t, response.Body.String(), "旧响应歌词")
		} else {
			require.NotContains(t, response.Body.String(), "旧响应歌词")
			require.NotContains(t, response.Body.String(), "waveform_peaks")
		}
	}
}

func TestSearchCatalogPublicAndLegacyVisibility(t *testing.T) {
	s, db, owner := newMusicTestService(t)
	other := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	admin := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleAdmin}
	for _, status := range []string{"active", "draft", "retired", "merged"} {
		require.NoError(t, db.Create(&model.Artist{Name: "隔离艺术家", LifecycleStatus: status, CreatedBy: &owner.ID}).Error)
		require.NoError(t, db.Create(&model.Album{Title: "隔离专辑", LifecycleStatus: status, UploadedBy: &owner.ID}).Error)
		require.NoError(t, db.Create(&model.Song{Title: "隔离歌曲", LifecycleStatus: status, UploadedBy: &owner.ID}).Error)
	}
	for _, public := range []bool{false, true} {
		require.NoError(t, db.Create(&model.Playlist{Name: "隔离歌单", UserID: owner.ID, IsPublic: public}).Error)
	}
	deleted := model.Song{Title: "隔离已删除", UploadedBy: &owner.ID}
	require.NoError(t, db.Create(&deleted).Error)
	require.NoError(t, db.Delete(&deleted).Error)
	for _, viewer := range []*authctx.CurrentUser{nil, &owner, &other, &admin} {
		got, err := s.SearchCatalog(context.Background(), viewer, MusicSearchOptions{Query: "隔离", ExactTotal: true, Visibility: MusicSearchPublic})
		require.NoError(t, err)
		for _, kind := range []string{"song", "album", "artist", "playlist"} {
			require.EqualValues(t, 1, got.Totals[kind], kind)
		}
		legacy, err := s.SearchCatalog(context.Background(), viewer, MusicSearchOptions{Query: "隔离", ExactTotal: true, Visibility: MusicSearchLegacy})
		require.NoError(t, err)
		entries, playlists := int64(1), int64(1)
		if viewer == &owner {
			entries, playlists = 2, 2
		}
		if viewer == &admin {
			entries = 4
		}
		for _, kind := range []string{"song", "album", "artist"} {
			require.Equal(t, entries, legacy.Totals[kind], kind)
		}
		require.Equal(t, playlists, legacy.Totals["playlist"])
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Request = httptest.NewRequest("GET", "/?q="+url.QueryEscape("隔离"), nil)
		if viewer != nil {
			authctx.SetCurrentUser(c, *viewer)
		}
		(&Handler{service: s}).search(c)
		require.Equal(t, 200, response.Code, response.Body.String())
		var envelope struct {
			Data musicSearchResponse `json:"data"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
		require.Equal(t, legacy.Totals, envelope.Data.Meta.Totals)
	}
}

func TestSearchCatalogHiddenRelationsAndDeletedSearchSources(t *testing.T) {
	s, db, owner := newMusicTestService(t)
	artist := model.Artist{Name: "秘密艺术家", LifecycleStatus: "draft", CreatedBy: &owner.ID}
	album := model.Album{Title: "秘密专辑", LifecycleStatus: "draft", UploadedBy: &owner.ID}
	require.NoError(t, db.Create(&artist).Error)
	require.NoError(t, db.Create(&album).Error)
	song := model.Song{Title: "公共歌曲", AlbumID: &album.ID}
	publicAlbum := model.Album{Title: "公共专辑"}
	require.NoError(t, db.Create(&song).Error)
	require.NoError(t, db.Create(&publicAlbum).Error)
	require.NoError(t, db.Create(&model.SongArtist{SongID: song.ID, ArtistID: artist.ID}).Error)
	require.NoError(t, db.Create(&model.AlbumArtist{AlbumID: publicAlbum.ID, ArtistID: artist.ID}).Error)
	got, err := s.SearchCatalog(context.Background(), &owner, MusicSearchOptions{Query: "秘密", ExactTotal: true})
	require.NoError(t, err)
	for _, total := range got.Totals {
		require.Zero(t, total)
	}
	got, err = s.SearchCatalog(context.Background(), &owner, MusicSearchOptions{Query: "公共", ExactTotal: true})
	require.NoError(t, err)
	require.Nil(t, got.Songs[0].Album)
	require.Empty(t, got.Songs[0].Artists)
	require.Empty(t, got.Albums[0].Artists)
	alias := model.ArtistAlias{ArtistID: artist.ID, Alias: "已删除别名"}
	lyrics := model.MusicSongLyric{SongID: song.ID, Content: "已删除歌词", Translation: "已删除翻译", UpdatedBy: owner.ID}
	require.NoError(t, db.Create(&alias).Error)
	require.NoError(t, db.Create(&lyrics).Error)
	require.NoError(t, db.Delete(&alias).Error)
	require.NoError(t, db.Delete(&lyrics).Error)
	got, err = s.SearchCatalog(context.Background(), &owner, MusicSearchOptions{Query: "已删除", ExactTotal: true, Visibility: MusicSearchLegacy})
	require.NoError(t, err)
	for _, total := range got.Totals {
		require.Zero(t, total)
	}
}

func TestSearchCatalogStablePagingCountsAndCancellation(t *testing.T) {
	s, db, owner := newMusicTestService(t)
	for i := range 4 {
		require.NoError(t, db.Create(&model.Artist{Name: "相同名称"}).Error)
		require.NoError(t, db.Create(&model.Album{Title: "相同名称"}).Error)
		require.NoError(t, db.Create(&model.Song{Title: "相同名称"}).Error)
		require.NoError(t, db.Create(&model.Playlist{Name: "相同名称", UserID: owner.ID, IsPublic: true, Description: fmt.Sprint(i)}).Error)
	}
	counts := 0
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:search_count", func(tx *gorm.DB) {
		if !tx.DryRun && strings.HasPrefix(strings.ToLower(tx.Statement.SQL.String()), "select count(*)") {
			counts++
		}
	}))
	for _, kind := range []string{"song", "album", "artist", "playlist"} {
		seen := map[uuid.UUID]bool{}
		for offset := range 4 {
			got, err := s.SearchCatalog(context.Background(), nil, MusicSearchOptions{Query: "相同", Type: kind, Limit: 1, Offset: offset, ExactTotal: true})
			require.NoError(t, err)
			require.EqualValues(t, 4, got.Totals[kind])
			var id uuid.UUID
			switch kind {
			case "song":
				id = got.Songs[0].ID
			case "album":
				id = got.Albums[0].ID
			case "artist":
				id = got.Artists[0].ID
			case "playlist":
				id = got.Playlists[0].ID
			}
			require.False(t, seen[id], kind)
			seen[id] = true
		}
	}
	require.Equal(t, 16, counts)
	counts = 0
	got, err := s.SearchCatalog(context.Background(), nil, MusicSearchOptions{Query: "相同", Limit: 3})
	require.NoError(t, err)
	require.Nil(t, got.Totals)
	require.Zero(t, counts, "preview/grouped 不计算精确总数")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.SearchCatalog(ctx, nil, MusicSearchOptions{Query: "相同"})
	require.ErrorIs(t, err, context.Canceled)
}
