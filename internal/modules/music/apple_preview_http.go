package music

import (
	"net/http"

	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// getAppleSongPreview godoc
// @Summary 获取 Apple Music 歌曲试听
// @Description 实时查询 iTunes 试听地址；试听地址不缓存、不持久化。
// @Tags music
// @Produce json
// @Param songId path string true "歌曲 ID"
// @Success 200 {object} AppleSongPreview
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/music/songs/{songId}/apple-preview [get]
func (h *Handler) getAppleSongPreview(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	songID, err := parseMusicID(c.Param("songId"), "songId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	preview, err := h.service.AppleSongPreview(c.Request.Context(), songID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, preview)
}
