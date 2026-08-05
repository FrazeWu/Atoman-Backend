package music

import (
	"errors"
	"net/http"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type musicSearchResponse struct {
	Songs     []model.Song     `json:"songs"`
	Albums    []model.Album    `json:"albums"`
	Artists   []model.Artist   `json:"artists"`
	Playlists []model.Playlist `json:"playlists"`
}

// search godoc
// @Summary 统一搜索音乐内容
// @Tags music
// @Produce json
// @Param q query string true "关键词"
// @Param type query string false "song,album,artist,playlist"
// @Router /api/v1/music/search [get]
func (h *Handler) search(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		httpx.OK(c, http.StatusOK, musicSearchResponse{Songs: []model.Song{}, Albums: []model.Album{}, Artists: []model.Artist{}, Playlists: []model.Playlist{}})
		return
	}
	page, pageSize := httpx.PageParams(c)
	offset := httpx.Offset(page, pageSize)
	pattern := "%" + query + "%"
	typeFilter := strings.TrimSpace(c.Query("type"))
	include := func(kind string) bool { return typeFilter == "" || typeFilter == kind }
	result := musicSearchResponse{Songs: []model.Song{}, Albums: []model.Album{}, Artists: []model.Artist{}, Playlists: []model.Playlist{}}

	if include("song") {
		if err := h.service.db.Model(&model.Song{}).
			Joins("LEFT JOIN \"Albums\" ON \"Albums\".id = \"Songs\".album_id").
			Joins("LEFT JOIN song_artists ON song_artists.song_id = \"Songs\".id").
			Joins("LEFT JOIN \"Artists\" ON \"Artists\".id = song_artists.artist_id").
			Where("\"Songs\".status NOT IN ?", []string{"closed", "rejected", "draft"}).
			Where("LOWER(\"Songs\".title) LIKE LOWER(?) OR LOWER(\"Albums\".title) LIKE LOWER(?) OR LOWER(\"Artists\".name) LIKE LOWER(?)", pattern, pattern, pattern).
			Distinct("\"Songs\".*").Preload("Album").Preload("Artists").Order("\"Songs\".play_count DESC, \"Songs\".title ASC").Limit(pageSize).Offset(offset).Find(&result.Songs).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		for i := range result.Songs {
			result.Songs[i].AudioURL = resolveMusicMediaURL(result.Songs[i].AudioURL)
			result.Songs[i].CoverURL = resolveMusicMediaURL(result.Songs[i].CoverURL)
		}
	}
	if include("album") {
		if err := h.service.db.Model(&model.Album{}).Joins("LEFT JOIN album_artists ON album_artists.album_id = \"Albums\".id").Joins("LEFT JOIN \"Artists\" ON \"Artists\".id = album_artists.artist_id").Where("COALESCE(\"Albums\".entry_status, '') <> ? AND COALESCE(\"Albums\".status, '') <> ?", "closed", "closed").Where("LOWER(\"Albums\".title) LIKE LOWER(?) OR LOWER(\"Artists\".name) LIKE LOWER(?)", pattern, pattern).Distinct("\"Albums\".*").Preload("Artists").Preload("Songs").Order("\"Albums\".hot_score DESC, \"Albums\".title ASC").Limit(pageSize).Offset(offset).Find(&result.Albums).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		for i := range result.Albums {
			resolveAlbumMediaURLs(&result.Albums[i])
		}
	}
	if include("artist") {
		if err := h.service.db.Where("COALESCE(entry_status, '') <> ?", "closed").Where("LOWER(name) LIKE LOWER(?) OR LOWER(legal_name) LIKE LOWER(?)", pattern, pattern).Order("name ASC").Limit(pageSize).Offset(offset).Find(&result.Artists).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		for i := range result.Artists {
			result.Artists[i].ImageURL = resolveMusicMediaURL(result.Artists[i].ImageURL)
		}
	}
	if include("playlist") {
		playlistDB := h.service.db.Where("LOWER(name) LIKE LOWER(?) AND is_public = ?", pattern, true)
		if user, ok := currentMusicUser(c); ok {
			playlistDB = playlistDB.Or("LOWER(name) LIKE LOWER(?) AND user_id = ?", pattern, user.ID)
		}
		if err := playlistDB.Order("updated_at DESC").Limit(pageSize).Offset(offset).Find(&result.Playlists).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		for i := range result.Playlists {
			result.Playlists[i].CoverURL = resolveMusicMediaURL(result.Playlists[i].CoverURL)
		}
	}
	httpx.OK(c, http.StatusOK, result)
}

type songArtistRoleResponse struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Role string    `json:"role"`
}
type songDetailResponse struct {
	Song       model.Song               `json:"song"`
	Artists    []songArtistRoleResponse `json:"artists"`
	Previous   *model.Song              `json:"previous,omitempty"`
	Next       *model.Song              `json:"next,omitempty"`
	Bookmarked bool                     `json:"bookmarked"`
	Playable   bool                     `json:"playable"`
}

// getSongDetail godoc
// @Summary 获取歌曲详情
// @Tags music
// @Produce json
// @Param songId path string true "歌曲 ID"
// @Router /api/v1/music/songs/{songId} [get]
func (h *Handler) getSongDetail(c *gin.Context) {
	songID, err := parseMusicID(c.Param("songId"), "songId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var song model.Song
	if err := h.service.db.Preload("Album.Artists").Preload("Artists").First(&song, "id = ? AND status NOT IN ?", songID, []string{"closed", "rejected", "draft"}).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			httpx.Error(c, apperr.NotFound("music.song_not_found", "Song not found"))
		} else {
			httpx.Error(c, err)
		}
		return
	}
	var links []model.SongArtist
	if err := h.service.db.Where("song_id = ?", song.ID).Find(&links).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	roles := make(map[uuid.UUID]string, len(links))
	for _, link := range links {
		roles[link.ArtistID] = link.Role
	}
	artists := make([]songArtistRoleResponse, 0, len(song.Artists))
	for _, artist := range song.Artists {
		artists = append(artists, songArtistRoleResponse{ID: artist.ID, Name: artist.Name, Role: roles[artist.ID]})
	}
	result := songDetailResponse{Song: song, Artists: artists, Playable: strings.TrimSpace(song.AudioURL) != ""}
	if user, ok := currentMusicUser(c); ok {
		var count int64
		if err := h.service.db.Model(&model.SongBookmark{}).Where("user_id = ? AND song_id = ?", user.ID, song.ID).Count(&count).Error; err == nil {
			result.Bookmarked = count > 0
		}
	}
	if song.AlbumID != nil {
		var previous, next model.Song
		h.service.db.Where("album_id = ? AND track_number < ? AND status NOT IN ?", *song.AlbumID, song.TrackNumber, []string{"closed", "rejected", "draft"}).Order("track_number DESC").First(&previous)
		h.service.db.Where("album_id = ? AND track_number > ? AND status NOT IN ?", *song.AlbumID, song.TrackNumber, []string{"closed", "rejected", "draft"}).Order("track_number ASC").First(&next)
		if previous.ID != uuid.Nil {
			previous.AudioURL = resolveMusicMediaURL(previous.AudioURL)
			result.Previous = &previous
		}
		if next.ID != uuid.Nil {
			next.AudioURL = resolveMusicMediaURL(next.AudioURL)
			result.Next = &next
		}
	}
	result.Song.AudioURL = resolveMusicMediaURL(result.Song.AudioURL)
	result.Song.CoverURL = resolveMusicMediaURL(result.Song.CoverURL)
	if result.Song.Album != nil {
		resolveAlbumMediaURLs(result.Song.Album)
	}
	httpx.OK(c, http.StatusOK, result)
}

// addToLaterPlaylist godoc
// @Summary 加入稍后播放
// @Tags music
// @Security BearerAuth
// @Param songId path string true "歌曲 ID"
// @Router /api/v1/music/playlists/later/{songId} [post]
func (h *Handler) addToLaterPlaylist(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	songID, err := parseMusicID(c.Param("songId"), "songId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var song model.Song
	if err := h.service.db.First(&song, "id = ? AND status NOT IN ?", songID, []string{"closed", "rejected", "draft"}).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("music.song_not_found", "Song not found"))
		} else {
			httpx.Error(c, err)
		}
		return
	}
	playlist := model.Playlist{UserID: user.ID, Name: "稍后播放", Kind: "later", IsPublic: false}
	if err := h.service.db.Where("user_id = ? AND kind = ?", user.ID, "later").FirstOrCreate(&playlist).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	if _, err := h.service.AddPlaylistSong(user, playlist.ID, songID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"playlist_id": playlist.ID, "added": true})
}

// library godoc
// @Summary 获取个人音乐库
// @Tags music
// @Security BearerAuth
// @Param kind query string false "song,album,artist,playlist"
// @Router /api/v1/music/library [get]
func (h *Handler) library(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	kind := c.DefaultQuery("kind", "song")
	page, pageSize := httpx.PageParams(c)
	switch kind {
	case "song":
		rows, total, err := h.service.ListSongBookmarks(user, page, pageSize, c.DefaultQuery("sort", "latest"))
		if err != nil {
			httpx.Error(c, err)
			return
		}
		httpx.List(c, rows, page, pageSize, total)
	case "album":
		rows, total, err := h.service.ListAlbumBookmarks(user, page, pageSize, c.DefaultQuery("sort", "latest"))
		if err != nil {
			httpx.Error(c, err)
			return
		}
		httpx.List(c, rows, page, pageSize, total)
	case "artist":
		rows, total, err := h.service.ListArtistBookmarks(user, page, pageSize, c.DefaultQuery("sort", "latest"))
		if err != nil {
			httpx.Error(c, err)
			return
		}
		httpx.List(c, rows, page, pageSize, total)
	case "playlist":
		rows, total, err := h.service.ListPlaylistBookmarks(user, page, pageSize, c.DefaultQuery("sort", "latest"))
		if err != nil {
			httpx.Error(c, err)
			return
		}
		httpx.List(c, rows, page, pageSize, total)
	default:
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "kind must be song, album, artist, or playlist"))
	}
}
