package books

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

type publicationRetentionHoldInput struct {
	Held   bool   `json:"held"`
	Reason string `json:"reason"`
}

// setPublicationRetentionHold godoc
// @Summary 设置或解除公共正文保全
// @Description 法律保全期间不会删除授权证据或申诉材料；设置和解除操作都会写入不可删除的审核日志。
// @Tags books-review
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param requestId path string true "公共正文申请 UUID"
// @Param input body publicationRetentionHoldInput true "保全状态和理由"
// @Success 200 {object} BookPublicationRetentionHoldDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/review/publication-requests/{requestId}/retention-hold [post]
func (h *Handler) setPublicationRetentionHold(c *gin.Context) {
	user, requestID, ok := publicationRequestRouteUser(c)
	if !ok {
		return
	}
	var input publicationRetentionHoldInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	hold, err := h.service.SetPublicationRetentionHold(user, requestID, input.Held, input.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, hold)
}
