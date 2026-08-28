package books

import (
	"net/http"

	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// retryBookImport godoc
// @Summary 重试失败的私有电子书处理
// @Description 仅普通处理失败可以重试；病毒隔离资源必须删除后重新提交。
// @Tags books-imports
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param importId path string true "导入会话 UUID"
// @Success 200 {object} BookImportSessionDTO
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/imports/{importId}/retry [post]
func (h *Handler) retryBookImport(c *gin.Context) {
	user, id, ok := bookImportRouteUser(c)
	if !ok {
		return
	}
	session, err := h.service.RetryBookImport(user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, session)
}
