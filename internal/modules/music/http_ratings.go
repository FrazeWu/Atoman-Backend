package music

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

type setSongRatingRequest struct {
	Score int `json:"score" binding:"required"`
}

type setAlbumRatingRequest struct {
	Score int `json:"score" binding:"required"`
}

// setAlbumRating godoc
// @Summary 评分专辑
// @Description 为可访问专辑设置 1 至 5 星评分；再次提交会更新原评分。
// @Tags music
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param albumId path string true "专辑 ID"
// @Param input body setAlbumRatingRequest true "评分"
// @Success 200 {object} AlbumRatingSummary
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/music/albums/{albumId}/rating [put]
func (h *Handler) setAlbumRating(c *gin.Context) {
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
	var input setAlbumRatingRequest
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	summary, err := h.service.SetAlbumRating(user, albumID, input.Score)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, summary)
}

// deleteAlbumRating godoc
// @Summary 清除专辑评分
// @Tags music
// @Produce json
// @Security BearerAuth
// @Param albumId path string true "专辑 ID"
// @Success 200 {object} AlbumRatingSummary
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/music/albums/{albumId}/rating [delete]
func (h *Handler) deleteAlbumRating(c *gin.Context) {
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
	if err := h.service.DeleteAlbumRating(user, albumID); err != nil {
		httpx.Error(c, err)
		return
	}
	summary, err := h.service.AlbumRatingSummary(albumID, &user.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, summary)
}

// setSongRating godoc
// @Summary 评分歌曲
// @Description 为可访问歌曲设置 1 至 5 星评分；再次提交会更新原评分。
// @Tags music
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param songId path string true "歌曲 ID"
// @Param input body setSongRatingRequest true "评分"
// @Success 200 {object} SongRatingSummary
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/music/songs/{songId}/rating [put]
func (h *Handler) setSongRating(c *gin.Context) {
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
	var input setSongRatingRequest
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	summary, err := h.service.SetSongRating(user, songID, input.Score)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, summary)
}

// deleteSongRating godoc
// @Summary 清除歌曲评分
// @Tags music
// @Produce json
// @Security BearerAuth
// @Param songId path string true "歌曲 ID"
// @Success 200 {object} SongRatingSummary
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/music/songs/{songId}/rating [delete]
func (h *Handler) deleteSongRating(c *gin.Context) {
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
	if err := h.service.DeleteSongRating(user, songID); err != nil {
		httpx.Error(c, err)
		return
	}
	summary, err := h.service.SongRatingSummary(songID, &user.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, summary)
}
