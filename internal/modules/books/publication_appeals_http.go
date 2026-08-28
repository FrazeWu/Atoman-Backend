package books

import (
	"net/http"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type submitPublicationAppealInput struct {
	Reason string `json:"reason"`
}

type reviewPublicationAppealInput struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

// submitPublicationAppeal godoc
// @Summary 申诉被下架的公共正文
// @Description 只有原公共正文申请人可以提交申诉；每个被下架资源同时只能有一个待处理申诉。
// @Tags books-publication
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param requestId path string true "公共正文申请 UUID"
// @Param input body submitPublicationAppealInput true "申诉理由"
// @Success 201 {object} BookPublicationAppealDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/publication-requests/{requestId}/appeals [post]
func (h *Handler) submitPublicationAppeal(c *gin.Context) {
	user, requestID, ok := publicationRequestRouteUser(c)
	if !ok {
		return
	}
	var input submitPublicationAppealInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	appeal, err := h.service.SubmitPublicationAppeal(user, requestID, input.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, appeal)
}

// listMyPublicationAppeals godoc
// @Summary 列出我的公共正文申诉
// @Tags books-publication
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param requestId path string true "公共正文申请 UUID"
// @Success 200 {object} BookPublicationAppealListResult
// @Router /api/v1/books/publication-requests/{requestId}/appeals [get]
func (h *Handler) listMyPublicationAppeals(c *gin.Context) {
	user, requestID, ok := publicationRequestRouteUser(c)
	if !ok {
		return
	}
	limit, offset := bookPagination(c)
	items, total, err := h.service.ListMyPublicationAppeals(c.Request.Context(), user, requestID, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, BookPublicationAppealListResult{Items: items, Total: total, Limit: limit, Offset: offset})
}

// listPublicationAppealReviewQueue godoc
// @Summary 列出公共正文申诉审核队列
// @Tags books-review
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Success 200 {object} BookPublicationAppealListResult
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Router /api/v1/books/review/publication-appeals [get]
func (h *Handler) listPublicationAppealReviewQueue(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset := bookPagination(c)
	items, total, err := h.service.ListPublicationAppealReviewQueue(c.Request.Context(), user, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, BookPublicationAppealListResult{Items: items, Total: total, Limit: limit, Offset: offset})
}

// reviewPublicationAppeal godoc
// @Summary 审核公共正文申诉
// @Description 通过申诉会恢复对应公共正文；驳回申诉会保持下架状态。
// @Tags books-review
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param appealId path string true "申诉 UUID"
// @Param input body reviewPublicationAppealInput true "审核决定"
// @Success 200 {object} BookPublicationAppealDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/review/publication-appeals/{appealId}/decision [post]
func (h *Handler) reviewPublicationAppeal(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	appealID, err := uuid.Parse(strings.TrimSpace(c.Param("appealId")))
	if err != nil || appealID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "appealId must be a valid UUID"))
		return
	}
	var input reviewPublicationAppealInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	appeal, err := h.service.ReviewPublicationAppeal(user, appealID, strings.TrimSpace(input.Decision), input.Note)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, appeal)
}
