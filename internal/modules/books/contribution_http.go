package books

import (
	"net/http"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type reviewBookEditInput struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

// submitBookEdit godoc
// @Summary 提交书目修订
// @Description 提交作品、版本或人物资料修订。每次申请至少需要一条可核验来源。
// @Tags books-contributions
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body SubmitBookEditInput true "书目修订"
// @Success 201 {object} BookEditDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Router /api/v1/books/contributions [post]
func (h *Handler) submitBookEdit(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	var input SubmitBookEditInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	edit, err := h.service.SubmitBookEdit(user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, edit)
}

// listMyBookEdits godoc
// @Summary 列出我的书目申请
// @Tags books-contributions
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Success 200 {object} BookEditListResult
// @Router /api/v1/books/contributions [get]
func (h *Handler) listMyBookEdits(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset := bookPagination(c)
	items, total, err := h.service.ListBookEdits(c.Request.Context(), user, false, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, BookEditListResult{Items: items, Total: total, Limit: limit, Offset: offset})
}

// withdrawBookEdit godoc
// @Summary 撤回书目申请
// @Tags books-contributions
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param editId path string true "申请 UUID"
// @Success 200 {object} map[string]boolean
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/contributions/{editId}/withdraw [post]
func (h *Handler) withdrawBookEdit(c *gin.Context) {
	user, editID, ok := bookEditRouteUser(c)
	if !ok {
		return
	}
	if err := h.service.WithdrawBookEdit(user, editID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"withdrawn": true})
}

// listBookEditReviewQueue godoc
// @Summary 列出待审核书目申请
// @Tags books-review
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Success 200 {object} BookEditListResult
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Router /api/v1/books/review/contributions [get]
func (h *Handler) listBookEditReviewQueue(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset := bookPagination(c)
	items, total, err := h.service.ListBookEdits(c.Request.Context(), user, true, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, BookEditListResult{Items: items, Total: total, Limit: limit, Offset: offset})
}

// reviewBookEdit godoc
// @Summary 审核书目申请
// @Description 审核者不能批准或驳回自己提交的申请。
// @Tags books-review
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param editId path string true "申请 UUID"
// @Param input body reviewBookEditInput true "审核决定"
// @Success 200 {object} BookEditDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/review/contributions/{editId}/decision [post]
func (h *Handler) reviewBookEdit(c *gin.Context) {
	user, editID, ok := bookEditRouteUser(c)
	if !ok {
		return
	}
	var input reviewBookEditInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	edit, err := h.service.ReviewBookEdit(user, editID, strings.TrimSpace(input.Decision), input.Note)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

type BookEditListResult struct {
	Items  []BookEditDTO `json:"items"`
	Total  int64         `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

func bookEditRouteUser(c *gin.Context) (authctx.CurrentUser, uuid.UUID, bool) {
	user, ok := currentBookUser(c)
	if !ok {
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	editID, err := uuid.Parse(strings.TrimSpace(c.Param("editId")))
	if err != nil || editID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "editId must be a valid UUID"))
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	return user, editID, true
}
