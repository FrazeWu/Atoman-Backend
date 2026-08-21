package music

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// convertSongToAlbum godoc
// @Summary 将独立歌曲转换为专辑
// @Tags music
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param songId path string true "歌曲 ID"
// @Param input body MusicReleaseConversionRequest true "发行资料"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/music/songs/{songId}/convert-to-album [post]
func (h *Handler) convertSongToAlbum(c *gin.Context) {
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
	var input MusicReleaseConversionRequest
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	albumID, err := h.service.ConvertStandaloneSongToAlbum(user, songID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"entity_type": "album", "id": albumID})
}

// convertAlbumToSong godoc
// @Summary 将单轨专辑转换为独立歌曲
// @Tags music
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param albumId path string true "专辑 ID"
// @Param input body MusicReleaseConversionRequest true "歌曲资料"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/music/albums/{albumId}/convert-to-song [post]
func (h *Handler) convertAlbumToSong(c *gin.Context) {
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
	var input MusicReleaseConversionRequest
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	songID, err := h.service.ConvertAlbumToStandaloneSong(user, albumID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"entity_type": "song", "id": songID})
}
