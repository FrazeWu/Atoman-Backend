package books

import (
	"net/http"
	"strconv"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// listBookShelf godoc
// @Summary 列出我的书架
// @Description 书架记录和书架备注只对当前用户可见。
// @Tags books-library
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param status query string false "want_to_read、reading、read、on_hold 或 dropped"
// @Param limit query int false "每页数量"
// @Param offset query int false "偏移量"
// @Success 200 {object} BookShelfListResult
// @Failure 401 {object} handlers.ErrorResponse
// @Router /api/v1/books/library [get]
func (h *Handler) listBookShelf(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset := bookPagination(c)
	items, total, err := h.service.ListBookShelf(c.Request.Context(), user, strings.TrimSpace(c.Query("status")), limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, BookShelfListResult{Items: items, Total: total, Limit: limit, Offset: offset})
}

// listContinueReading godoc
// @Summary 列出继续阅读项目
// @Description 只返回当前用户拥有且尚未读完的私有资源，不返回其他用户或公共资源信息。
// @Tags books-library
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param limit query int false "数量"
// @Success 200 {array} BookContinueReadingDTO
// @Failure 401 {object} handlers.ErrorResponse
// @Router /api/v1/books/library/continue [get]
func (h *Handler) listContinueReading(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, _ := bookPagination(c)
	items, err := h.service.ListContinueReading(c.Request.Context(), user, limit)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, items)
}

// saveBookShelf godoc
// @Summary 保存书架状态
// @Description 仅已公开的作品可以加入当前用户的私有书架。
// @Tags books-library
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Param input body SaveBookShelfInput true "书架状态"
// @Success 200 {object} BookShelfItemDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/library/works/{workId} [put]
func (h *Handler) saveBookShelf(c *gin.Context) {
	user, workID, ok := bookWorkRouteUser(c)
	if !ok {
		return
	}
	var input SaveBookShelfInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	item, err := h.service.SaveBookShelf(user, workID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, item)
}

// deleteBookShelf godoc
// @Summary 移出书架
// @Tags books-library
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Success 200 {object} map[string]boolean
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Router /api/v1/books/library/works/{workId} [delete]
func (h *Handler) deleteBookShelf(c *gin.Context) {
	user, workID, ok := bookWorkRouteUser(c)
	if !ok {
		return
	}
	if err := h.service.DeleteBookShelf(user, workID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

func bookWorkRouteUser(c *gin.Context) (authctx.CurrentUser, uuid.UUID, bool) {
	user, ok := currentBookUser(c)
	if !ok {
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	workID, err := uuid.Parse(strings.TrimSpace(c.Param("workId")))
	if err != nil || workID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "workId must be a valid UUID"))
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	return user, workID, true
}

func bookPagination(c *gin.Context) (int, int) {
	limit := 20
	offset := 0
	if value, err := strconv.Atoi(c.Query("limit")); err == nil && value > 0 {
		limit = value
	}
	if value, err := strconv.Atoi(c.Query("offset")); err == nil && value >= 0 {
		offset = value
	}
	return normalizeCatalogPagination(limit, offset)
}
