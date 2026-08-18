package music

import (
	"errors"
	"net/http"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	revisionservice "atoman/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type musicSearchResponse struct {
	Songs     []model.Song     `json:"songs"`
	Albums    []model.Album    `json:"albums"`
	Artists   []model.Artist   `json:"artists"`
	Playlists []model.Playlist `json:"playlists"`
	Meta      musicSearchMeta  `json:"meta"`
}

type musicSearchMeta struct {
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
	Totals   map[string]int64 `json:"totals"`
	HasMore  map[string]bool  `json:"has_more"`
}

type recordSearchInteractionRequest struct {
	Query      string    `json:"query"`
	EntityType string    `json:"entity_type" binding:"required"`
	EntityID   uuid.UUID `json:"entity_id" binding:"required"`
}

// search godoc
// @Summary 统一搜索音乐内容
// @Tags music
// @Produce json
// @Param q query string true "关键词"
// @Param type query string false "song,album,artist,playlist"
// @Param page query int false "页码"
// @Param page_size query int false "每页数量"
// @Success 200 {object} musicSearchResponse
// @Router /api/v1/music/search [get]
func (h *Handler) search(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	newResult := func() musicSearchResponse {
		return musicSearchResponse{
			Songs: []model.Song{}, Albums: []model.Album{}, Artists: []model.Artist{}, Playlists: []model.Playlist{},
			Meta: musicSearchMeta{Page: page, PageSize: pageSize, Totals: map[string]int64{"song": 0, "album": 0, "artist": 0, "playlist": 0}, HasMore: map[string]bool{"song": false, "album": false, "artist": false, "playlist": false}},
		}
	}
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		httpx.OK(c, http.StatusOK, newResult())
		return
	}
	offset := httpx.Offset(page, pageSize)
	pattern := "%" + query + "%"
	typeFilter := strings.TrimSpace(c.Query("type"))
	include := func(kind string) bool { return typeFilter == "" || typeFilter == kind }
	result := newResult()
	viewer, hasViewer := currentMusicUser(c)
	viewerPtr := musicViewer(viewer, hasViewer)

	if include("song") {
		var total int64
		songQuery := func() *gorm.DB {
			return scopeVisibleMusicEntries(h.service.db.Model(&model.Song{}), "\"Songs\"", "uploaded_by", viewerPtr, false).
				Joins("LEFT JOIN \"Albums\" ON \"Albums\".id = \"Songs\".album_id").
				Joins("LEFT JOIN song_artists ON song_artists.song_id = \"Songs\".id").
				Joins("LEFT JOIN \"Artists\" ON \"Artists\".id = song_artists.artist_id").
				Where("LOWER(\"Songs\".title) LIKE LOWER(?) OR LOWER(\"Albums\".title) LIKE LOWER(?) OR LOWER(\"Artists\".name) LIKE LOWER(?)", pattern, pattern, pattern)
		}
		if err := songQuery().Distinct("\"Songs\".id").Count(&total).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		result.Meta.Totals["song"] = total
		if err := songQuery().
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
		var total int64
		albumQuery := func() *gorm.DB {
			return scopeVisibleMusicEntries(h.service.db.Model(&model.Album{}), "\"Albums\"", "uploaded_by", viewerPtr, false).Joins("LEFT JOIN album_artists ON album_artists.album_id = \"Albums\".id").Joins("LEFT JOIN \"Artists\" ON \"Artists\".id = album_artists.artist_id").Where("LOWER(\"Albums\".title) LIKE LOWER(?) OR LOWER(\"Artists\".name) LIKE LOWER(?)", pattern, pattern)
		}
		if err := albumQuery().Distinct("\"Albums\".id").Count(&total).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		result.Meta.Totals["album"] = total
		if err := albumQuery().Distinct("\"Albums\".*").Preload("Artists").Preload("Songs", visibleSongPreload(viewerPtr)).Order("\"Albums\".hot_score DESC, \"Albums\".title ASC").Limit(pageSize).Offset(offset).Find(&result.Albums).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		for i := range result.Albums {
			resolveAlbumMediaURLs(&result.Albums[i])
		}
	}
	if include("artist") {
		var total int64
		artistQuery := func() *gorm.DB {
			return scopeVisibleMusicEntries(h.service.db.Model(&model.Artist{}), "\"Artists\"", "created_by", viewerPtr, false).Where("LOWER(name) LIKE LOWER(?) OR LOWER("+artistDisambiguationSearchExpression+") LIKE LOWER(?) OR LOWER(legal_name) LIKE LOWER(?)", pattern, pattern, pattern)
		}
		if err := artistQuery().Count(&total).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		result.Meta.Totals["artist"] = total
		if err := artistQuery().Order("name ASC").Limit(pageSize).Offset(offset).Find(&result.Artists).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		for i := range result.Artists {
			result.Artists[i].ImageURL = resolveMusicMediaURL(result.Artists[i].ImageURL)
		}
	}
	if include("playlist") {
		var total int64
		playlistQuery := func() *gorm.DB {
			visible := h.service.db.Model(&model.Playlist{}).Where("LOWER(name) LIKE LOWER(?) AND is_public = ?", pattern, true)
			if user, ok := currentMusicUser(c); ok {
				visible = visible.Or("LOWER(name) LIKE LOWER(?) AND user_id = ?", pattern, user.ID)
			}
			return visible
		}
		if err := playlistQuery().Count(&total).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		result.Meta.Totals["playlist"] = total
		if err := playlistQuery().Order("updated_at DESC").Limit(pageSize).Offset(offset).Find(&result.Playlists).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		for i := range result.Playlists {
			result.Playlists[i].CoverURL = resolveMusicMediaURL(result.Playlists[i].CoverURL)
		}
	}
	for kind, total := range result.Meta.Totals {
		result.Meta.HasMore[kind] = int64(page*pageSize) < total
	}
	httpx.OK(c, http.StatusOK, result)
}

// recordSearchInteraction godoc
// @Summary 记录音乐搜索点击
// @Tags music
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body recordSearchInteractionRequest true "搜索点击"
// @Success 204
// @Router /api/v1/music/search/interactions [post]
func (h *Handler) recordSearchInteraction(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var input recordSearchInteractionRequest
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	input.Query = strings.TrimSpace(input.Query)
	switch input.EntityType {
	case "song", "album", "artist", "playlist":
	default:
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "invalid entity_type"))
		return
	}
	if input.EntityID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "entity_id is required"))
		return
	}
	interaction := model.MusicSearchInteraction{UserID: user.ID, Query: input.Query, EntityType: input.EntityType, EntityID: input.EntityID}
	if err := h.service.db.Create(&interaction).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type songArtistRoleResponse struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	Role       string    `json:"role"`
	CustomRole string    `json:"custom_role,omitempty"`
	Position   int       `json:"position"`
}
type songDetailResponse struct {
	Song     model.Song               `json:"song"`
	Artists  []songArtistRoleResponse `json:"artists"`
	Previous *model.Song              `json:"previous,omitempty"`
	Next     *model.Song              `json:"next,omitempty"`
	Playable bool                     `json:"playable"`
}

type createSongAudioReplacementRequest struct {
	AssetID uuid.UUID `json:"asset_id" binding:"required"`
}

// createSongAudioReplacement godoc
// @Summary 排队替换歌曲音频
// @Description 仅接受当前用户已完成的本地音频上传 asset_id；后台成功后原子切换，失败时保留原音频。
// @Tags music
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param songId path string true "歌曲 ID"
// @Param input body createSongAudioReplacementRequest true "已完成的音频上传资产"
// @Success 202 {object} model.SongAudioReplacement
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/music/songs/{songId}/audio-replacements [post]
func (h *Handler) createSongAudioReplacement(c *gin.Context) {
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
	var input createSongAudioReplacementRequest
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	if input.AssetID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "asset_id is required"))
		return
	}
	if err := revisionservice.ValidateMusicEntryEdit(h.service.db, "song", songID, user.ID, "audio_url"); err != nil {
		httpx.Error(c, err)
		return
	}
	var asset model.MediaAsset
	if err := h.service.db.First(&asset, "id = ? AND user_id = ? AND purpose = ?", input.AssetID, user.ID, "music.audio").Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("music.audio_asset_not_found", "Completed audio upload not found"))
		} else {
			httpx.Error(c, err)
		}
		return
	}
	if !isMusicAudioContentType(asset.ContentType) || strings.TrimSpace(asset.URL) == "" || strings.TrimSpace(asset.Key) == "" {
		httpx.Error(c, apperr.Unprocessable("music.audio_asset_invalid", "Uploaded asset is not valid audio"))
		return
	}
	var song model.Song
	if err := h.service.db.First(&song, "id = ?", songID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("music.song_not_found", "Song not found"))
		} else {
			httpx.Error(c, err)
		}
		return
	}
	job := model.SongAudioReplacement{
		SongID: songID, RequestedBy: user.ID, AssetID: &asset.ID, AudioURL: asset.URL,
		SourceKey: asset.Key, PreviousAudioURL: song.AudioURL, Status: "pending",
	}
	if err := h.service.db.Create(&job).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusAccepted, job)
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
	viewer, hasViewer := currentMusicUser(c)
	viewerPtr := musicViewer(viewer, hasViewer)
	var song model.Song
	query := scopeVisibleMusicEntries(h.service.db, "\"Songs\"", "uploaded_by", viewerPtr, false)
	if err := query.Preload("Album.Artists").Preload("Artists").First(&song, "\"Songs\".id = ?", songID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			httpx.Error(c, apperr.NotFound("music.song_not_found", "Song not found"))
		} else {
			httpx.Error(c, err)
		}
		return
	}
	var links []model.SongArtist
	if err := h.service.db.Preload("Artist").Where("song_id = ?", song.ID).Order("position ASC, created_at ASC").Find(&links).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	artists := make([]songArtistRoleResponse, 0, len(links))
	for _, link := range links {
		name := ""
		if link.Artist != nil {
			name = link.Artist.Name
		}
		artists = append(artists, songArtistRoleResponse{
			ID: link.ArtistID, Name: name, Role: link.Role,
			CustomRole: link.CustomRole, Position: link.Position,
		})
	}
	result := songDetailResponse{Song: song, Artists: artists, Playable: strings.TrimSpace(song.AudioURL) != ""}
	if song.AlbumID != nil {
		previous, next := loadAdjacentAlbumSongs(h.service.db, song)
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

func loadAdjacentAlbumSongs(db *gorm.DB, song model.Song) (model.Song, model.Song) {
	if song.AlbumID == nil {
		return model.Song{}, model.Song{}
	}
	var previous, next model.Song
	discNumber := normalizedDiscNumber(song.DiscNumber)
	visibleStatus := model.MusicLifecycleActive
	db.Where(
		"album_id = ? AND lifecycle_status = ? AND (COALESCE(NULLIF(disc_number, 0), 1) < ? OR (COALESCE(NULLIF(disc_number, 0), 1) = ? AND track_number < ?))",
		*song.AlbumID, visibleStatus, discNumber, discNumber, song.TrackNumber,
	).Order("COALESCE(NULLIF(disc_number, 0), 1) DESC, track_number DESC, created_at DESC").First(&previous)
	db.Where(
		"album_id = ? AND lifecycle_status = ? AND (COALESCE(NULLIF(disc_number, 0), 1) > ? OR (COALESCE(NULLIF(disc_number, 0), 1) = ? AND track_number > ?))",
		*song.AlbumID, visibleStatus, discNumber, discNumber, song.TrackNumber,
	).Order("COALESCE(NULLIF(disc_number, 0), 1) ASC, track_number ASC, created_at ASC").First(&next)
	return previous, next
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
	if err := h.service.db.First(&song, "id = ? AND lifecycle_status = ?", songID, model.MusicLifecycleActive).Error; err != nil {
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

// deleteFromLaterPlaylist godoc
// @Summary 移出稍后播放
// @Tags music
// @Security BearerAuth
// @Param songId path string true "歌曲 ID"
// @Success 200 {object} map[string]bool
// @Router /api/v1/music/playlists/later/{songId} [delete]
func (h *Handler) deleteFromLaterPlaylist(c *gin.Context) {
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
	var playlist model.Playlist
	if err := h.service.db.Where("user_id = ? AND kind = ?", user.ID, "later").First(&playlist).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
			return
		}
		httpx.Error(c, err)
		return
	}
	if err := h.service.DeletePlaylistSong(user, playlist.ID, songID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

// library godoc
// @Summary 获取个人音乐库
// @Tags music
// @Security BearerAuth
// @Param kind query string false "album,artist,playlist,later"
// @Param q query string false "关键词"
// @Param sort query string false "latest,popular,name"
// @Router /api/v1/music/library [get]
func (h *Handler) library(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	kind := c.DefaultQuery("kind", "album")
	query := c.Query("q")
	sort := c.DefaultQuery("sort", "latest")
	page, pageSize := httpx.PageParams(c)
	switch kind {
	case "album":
		rows, total, err := h.service.ListAlbumBookmarksFiltered(user, page, pageSize, sort, query)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		httpx.List(c, rows, page, pageSize, total)
	case "artist":
		rows, total, err := h.service.ListArtistBookmarksFiltered(user, page, pageSize, sort, query)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		httpx.List(c, rows, page, pageSize, total)
	case "playlist":
		rows, total, err := h.service.ListPlaylistBookmarksFiltered(user, page, pageSize, sort, query)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		httpx.List(c, rows, page, pageSize, total)
	case "later":
		rows, total, err := h.service.ListLaterSongs(user, page, pageSize, sort, query)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		httpx.List(c, rows, page, pageSize, total)
	default:
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "kind must be album, artist, playlist, or later"))
	}
}
