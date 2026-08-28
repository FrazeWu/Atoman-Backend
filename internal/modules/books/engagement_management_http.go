package books

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

type BookEditVoteInput struct {
	Value int `json:"value"`
}

// getMyBookReview godoc
// @Summary 获取我的短书评
// @Tags books-engagement
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Success 200 {object} BookReviewDTO
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/works/{workId}/review [get]
func (h *Handler) getMyBookReview(c *gin.Context) {
	user, workID, ok := bookWorkRouteUser(c)
	if !ok {
		return
	}
	review, err := h.service.GetBookReview(user, workID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, review)
}

// @Summary 删除自己的短书评
// @Tags books-engagement
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Success 200 {object} map[string]boolean
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/works/{workId}/review [delete]
func (h *Handler) deleteBookReview(c *gin.Context) {
	user, workID, ok := bookWorkRouteUser(c)
	if !ok {
		return
	}
	if err := h.service.DeleteBookReview(user, workID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

// voteBookEdit godoc
// @Summary 对书目申请投票
// @Tags books-contributions
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param editId path string true "申请 UUID"
// @Param input body BookEditVoteInput true "支持或反对"
// @Success 200 {object} BookEditDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/contributions/{editId}/vote [post]
func (h *Handler) voteBookEdit(c *gin.Context) {
	user, editID, ok := bookEditRouteUser(c)
	if !ok {
		return
	}
	var input BookEditVoteInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	edit, err := h.service.VoteBookEdit(user, editID, input.Value)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}
