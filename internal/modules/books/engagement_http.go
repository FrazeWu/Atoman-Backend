package books

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// listPublicBookReviews godoc
// @Summary 列出公共作品短书评
// @Tags books-engagement
// @Produce json
// @Param workId path string true "公共作品 UUID"
// @Param limit query int false "每页数量，最大 50"
// @Param offset query int false "偏移量"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/works/{workId}/reviews [get]
func (h *Handler) listPublicBookReviews(c *gin.Context) {
	workID, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	reviews, total, err := h.service.ListPublicBookReviews(c.Request.Context(), workID, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": reviews, "total": total, "limit": limit, "offset": offset})
}

// setBookRating godoc
// @Summary 为公共作品评分
// @Tags books-engagement
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "公共作品 UUID"
// @Param input body bookRatingInput true "1 至 10 分，每 1 分对应半星"
// @Success 200 {object} BookRatingSummary
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/works/{workId}/rating [put]
func (h *Handler) setBookRating(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	workID, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	var input bookRatingInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	summary, err := h.service.SetBookRating(user, workID, input.Score)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, summary)
}

// getBookRating godoc
// @Summary 获取当前用户的公共作品评分
// @Tags books-engagement
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "公共作品 UUID"
// @Success 200 {object} BookRatingSummary
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/works/{workId}/rating [get]
func (h *Handler) getBookRating(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	workID, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	summary, err := h.service.BookRatingSummary(c.Request.Context(), workID, &user.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, summary)
}

// deleteBookRating godoc
// @Summary 清除当前用户的公共作品评分
// @Tags books-engagement
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "公共作品 UUID"
// @Success 200 {object} BookRatingSummary
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/works/{workId}/rating [delete]
func (h *Handler) deleteBookRating(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	workID, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	if err := h.service.DeleteBookRating(user, workID); err != nil {
		httpx.Error(c, err)
		return
	}
	summary, err := h.service.BookRatingSummary(c.Request.Context(), workID, &user.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, summary)
}

// saveBookReview godoc
// @Summary 创建或更新公共作品短书评
// @Tags books-engagement
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "公共作品 UUID"
// @Param input body SaveBookReviewInput true "短书评"
// @Success 200 {object} BookReviewDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/works/{workId}/review [put]
func (h *Handler) saveBookReview(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	workID, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	var input SaveBookReviewInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	review, err := h.service.SaveBookReview(user, workID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, review)
}

type bookRatingInput struct {
	Score int `json:"score"`
}
