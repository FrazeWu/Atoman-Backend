package music

import (
	"net/http"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *Handler) listArtistBookmarks(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	sort := c.DefaultQuery("sort", "latest")
	bookmarks, total, err := h.service.ListArtistBookmarks(user, page, pageSize, sort)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, bookmarks, page, pageSize, total)
}

func (h *Handler) createArtistBookmark(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreateArtistBookmarkRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	bookmark, err := h.service.BookmarkArtist(user, req.ArtistID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, bookmark)
}

func (h *Handler) deleteArtistBookmark(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	artistID, err := parseMusicID(c.Param("artistId"), "artistId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.DeleteArtistBookmark(user, artistID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

func (h *Handler) listAlbumBookmarks(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	sort := c.DefaultQuery("sort", "latest")
	bookmarks, total, err := h.service.ListAlbumBookmarks(user, page, pageSize, sort)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, bookmarks, page, pageSize, total)
}

func (h *Handler) createAlbumBookmark(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreateAlbumBookmarkRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	bookmark, err := h.service.BookmarkAlbum(user, req.AlbumID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, bookmark)
}

func (h *Handler) deleteAlbumBookmark(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	albumID, err := parseMusicID(c.Param("albumId"), "albumId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.DeleteAlbumBookmark(user, albumID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

func (h *Handler) listSongBookmarks(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	sort := c.DefaultQuery("sort", "latest")
	bookmarks, total, err := h.service.ListSongBookmarks(user, page, pageSize, sort)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, bookmarks, page, pageSize, total)
}

func (h *Handler) createSongBookmark(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreateSongBookmarkRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	bookmark, err := h.service.BookmarkSong(user, req.SongID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, bookmark)
}

func (h *Handler) deleteSongBookmark(c *gin.Context) {
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
	if err := h.service.DeleteSongBookmark(user, songID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

// listPlaylistBookmarks godoc
// @Summary 获取当前用户收藏的歌单
// @Tags music-bookmarks
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Success 200 {object} PlaylistBookmarkListResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Router /api/v1/music/bookmarks/playlists [get]
func (h *Handler) listPlaylistBookmarks(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	sort := c.DefaultQuery("sort", "latest")
	bookmarks, total, err := h.service.ListPlaylistBookmarks(user, page, pageSize, sort)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	for index := range bookmarks {
		if bookmarks[index].Playlist != nil {
			bookmarks[index].Playlist.CoverURL = resolveMusicMediaURL(bookmarks[index].Playlist.CoverURL)
		}
	}
	httpx.List(c, bookmarks, page, pageSize, total)
}

// createPlaylistBookmark godoc
// @Summary 收藏歌单
// @Tags music-bookmarks
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param body body CreatePlaylistBookmarkRequest true "歌单"
// @Success 201 {object} model.PlaylistBookmark
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/music/bookmarks/playlists [post]
func (h *Handler) createPlaylistBookmark(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreatePlaylistBookmarkRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	bookmark, err := h.service.BookmarkPlaylist(user, req.PlaylistID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, bookmark)
}

// deletePlaylistBookmark godoc
// @Summary 取消收藏歌单
// @Tags music-bookmarks
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param playlistId path string true "歌单 ID"
// @Success 200 {object} map[string]bool
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Router /api/v1/music/bookmarks/playlists/{playlistId} [delete]
func (h *Handler) deletePlaylistBookmark(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	playlistID, err := parseMusicID(c.Param("playlistId"), "playlistId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.DeletePlaylistBookmark(user, playlistID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

func (h *Handler) listPlaylists(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	sort := c.DefaultQuery("sort", "latest")
	playlists, total, err := h.service.ListPlaylists(user, page, pageSize, sort)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	playlistIDs := make([]uuid.UUID, 0, len(playlists))
	for _, playlist := range playlists {
		playlistIDs = append(playlistIDs, playlist.ID)
	}
	songCounts, err := h.service.repo.CountPlaylistSongs(playlistIDs)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	rows := make([]PlaylistSummaryResponse, 0, len(playlists))
	for _, playlist := range playlists {
		rows = append(rows, PlaylistSummaryResponse{
			ID:          playlist.ID,
			UserID:      playlist.UserID,
			Name:        playlist.Name,
			Description: playlist.Description,
			CoverURL:    resolveMusicMediaURL(playlist.CoverURL),
			IsPublic:    playlist.IsPublic,
			IsFavorite:  playlist.IsFavorite,
			SongCount:   songCounts[playlist.ID],
		})
	}
	httpx.List(c, rows, page, pageSize, total)
}

// listPublicPlaylists godoc
// @Summary 获取公开歌单列表
// @Description 返回可被发现的公开歌单，匿名用户可访问。
// @Tags music-playlists
// @Produce json
// @Success 200 {object} PlaylistSummaryListResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/music/playlists/public [get]
func (h *Handler) listPublicPlaylists(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	playlists, songCounts, total, err := h.service.ListPublicPlaylists(page, pageSize)
	if err != nil {
		httpx.Error(c, err)
		return
	}

	rows := make([]PlaylistSummaryResponse, 0, len(playlists))
	for _, playlist := range playlists {
		rows = append(rows, PlaylistSummaryResponse{
			ID:          playlist.ID,
			UserID:      playlist.UserID,
			Name:        playlist.Name,
			Description: playlist.Description,
			CoverURL:    resolveMusicMediaURL(playlist.CoverURL),
			IsPublic:    playlist.IsPublic,
			IsFavorite:  playlist.IsFavorite,
			SongCount:   songCounts[playlist.ID],
		})
	}
	httpx.List(c, rows, page, pageSize, total)
}

func buildPlaylistSummaryResponse(playlist model.Playlist, songCount int64) PlaylistSummaryResponse {
	return PlaylistSummaryResponse{
		ID:          playlist.ID,
		UserID:      playlist.UserID,
		Name:        playlist.Name,
		Description: playlist.Description,
		CoverURL:    resolveMusicMediaURL(playlist.CoverURL),
		IsPublic:    playlist.IsPublic,
		IsFavorite:  playlist.IsFavorite,
		SongCount:   songCount,
	}
}

// createPlaylist godoc
// @Summary 创建歌单
// @Description 创建歌单，可同时设置简介、封面和是否公开。
// @Tags music-playlists
// @Accept json
// @Produce json
// @Param input body CreatePlaylistRequest true "歌单创建请求"
// @Success 201 {object} PlaylistSummaryResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/playlists [post]
func (h *Handler) createPlaylist(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreatePlaylistRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	playlist, err := h.service.CreatePlaylist(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, buildPlaylistSummaryResponse(playlist, 0))
}

func (h *Handler) deletePlaylist(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.DeletePlaylist(user, playlistID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

// getPlaylist godoc
// @Summary 获取歌单详情
// @Description 返回歌单详情。公开歌单支持匿名访问，私有歌单仅所有者可访问。
// @Tags music-playlists
// @Produce json
// @Param id path string true "歌单 ID"
// @Success 200 {object} PlaylistSummaryResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/music/playlists/{id} [get]
func (h *Handler) getPlaylist(c *gin.Context) {
	user, _ := currentMusicUser(c)
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	playlist, err := h.service.GetPlaylist(user, playlistID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildPlaylistSummaryResponse(playlist, 0))
}

func (h *Handler) updatePlaylist(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req UpdatePlaylistRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	playlist, err := h.service.UpdatePlaylist(user, playlistID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildPlaylistSummaryResponse(playlist, 0))
}

// listPlaylistSongs godoc
// @Summary 获取歌单歌曲列表
// @Description 返回歌单中的歌曲列表。公开歌单支持匿名访问，私有歌单仅所有者可访问。
// @Tags music-playlists
// @Produce json
// @Param id path string true "歌单 ID"
// @Success 200 {object} PlaylistSongsListResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/music/playlists/{id}/songs [get]
func (h *Handler) listPlaylistSongs(c *gin.Context) {
	user, _ := currentMusicUser(c)
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	page, pageSize := httpx.PageParams(c)
	songs, total, err := h.service.ListPlaylistSongs(user, playlistID, page, pageSize)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, songs, page, pageSize, total)
}

func (h *Handler) addPlaylistSong(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req AddPlaylistSongRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	playlistSong, err := h.service.AddPlaylistSong(user, playlistID, req.SongID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, playlistSong)
}

// reorderPlaylistSongs godoc
// @Summary 调整歌单歌曲顺序
// @Description 使用完整歌曲 ID 列表更新歌单顺序，支持普通歌单和最爱歌单。
// @Tags music-playlists
// @Accept json
// @Produce json
// @Param id path string true "歌单 ID"
// @Param input body ReorderPlaylistSongsRequest true "完整歌曲顺序"
// @Success 200 {object} map[string]bool
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/playlists/{id}/songs/order [put]
func (h *Handler) reorderPlaylistSongs(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req ReorderPlaylistSongsRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.ReorderPlaylistSongs(user, playlistID, req.SongIDs); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"reordered": true})
}

func (h *Handler) deletePlaylistSong(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	songID, err := parseMusicID(c.Param("songId"), "songId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.DeletePlaylistSong(user, playlistID, songID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}
