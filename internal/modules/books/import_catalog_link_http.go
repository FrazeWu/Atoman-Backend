package books

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// linkBookImportToCatalog godoc
// @Summary 关联私有导入与公共书目
// @Description 只保存当前用户导入与已公开作品或版本的关联，不会改变私有正文的可见性。
// @Tags books-imports
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param importId path string true "导入会话 UUID"
// @Param input body LinkBookImportInput true "公共书目关联"
// @Success 200 {object} BookImportSessionDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/imports/{importId}/catalog-link [put]
func (h *Handler) linkBookImportToCatalog(c *gin.Context) {
	user, importID, ok := bookImportRouteUser(c)
	if !ok {
		return
	}
	var input LinkBookImportInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	session, err := h.service.LinkBookImportToCatalog(user, importID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, session)
}
