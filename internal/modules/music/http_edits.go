package music

import (
	"errors"
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (h *Handler) listEdits(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		httpx.Error(c, apperr.Forbidden("music.edit_forbidden", "Moderator role required"))
		return
	}

	page, pageSize := httpx.PageParams(c)
	edits, total, err := h.service.repo.ListEdits(ListEditsQuery{
		Status:     c.Query("status"),
		EntityType: c.Query("entity_type"),
		Type:       c.Query("type"),
		Page:       page,
		PageSize:   pageSize,
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}

	httpx.List(c, edits, page, pageSize, total)
}

func (h *Handler) submitEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	var req SubmitEditRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}

	edit, err := h.service.SubmitEdit(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, edit)
}

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

func (h *Handler) getEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	edit, err := h.service.repo.GetEdit(editID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("music.edit_not_found", "Edit not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	if edit.SubmittedBy != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		httpx.Error(c, apperr.Forbidden("music.edit_forbidden", "You cannot view this edit"))
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func (h *Handler) voteEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req VoteRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.Vote(user, editID, req); err != nil {
		httpx.Error(c, err)
		return
	}

	edit, err := h.service.repo.GetEdit(editID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func (h *Handler) approveEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req DecisionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	edit, err := h.service.ApproveEdit(user, editID, req.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func (h *Handler) rejectEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req DecisionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	edit, err := h.service.RejectEdit(user, editID, req.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func (h *Handler) cancelEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req DecisionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	edit, err := h.service.CancelEdit(user, editID, req.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func parseEditID(raw string) (uuid.UUID, error) {
	return parseMusicID(raw, "editId")
}
