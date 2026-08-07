package music

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// submitAlbumMerge godoc
// @Summary 合并重复专辑
// @Description 管理员确认曲目对应关系后直接合并，源专辑保留旧链接重定向。
// @Tags music
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param albumId path string true "目标专辑 ID"
// @Param input body AlbumMergeRequest true "合并确认"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/music/albums/{albumId}/merge [post]
func (h *Handler) submitAlbumMerge(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		httpx.Error(c, apperr.Forbidden("music.merge_forbidden", "Admin role required"))
		return
	}

	targetAlbumID, err := parseMusicID(c.Param("albumId"), "albumId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req AlbumMergeRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	if !req.Confirmed {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "confirmed must be true"))
		return
	}
	if err := h.service.MergeAlbums(user, targetAlbumID, req.SourceAlbumID, req.SongMatches); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"merged": true, "redirect_to": targetAlbumID})
}

// previewAlbumMerge godoc
// @Summary 预览重复专辑合并
// @Description 按规范化曲名、碟号和曲序预匹配源专辑与目标专辑曲目。
// @Tags music
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param albumId path string true "目标专辑 ID"
// @Param input body AlbumMergeRequest true "源专辑"
// @Success 200 {object} AlbumMergePreviewResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/music/albums/{albumId}/merge/preview [post]
func (h *Handler) previewAlbumMerge(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		httpx.Error(c, apperr.Forbidden("music.merge_forbidden", "Admin role required"))
		return
	}
	targetAlbumID, err := parseMusicID(c.Param("albumId"), "albumId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req AlbumMergeRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	preview, err := h.service.PreviewAlbumMerge(targetAlbumID, req.SourceAlbumID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, preview)
}
