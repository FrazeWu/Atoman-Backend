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

type BookPostListResult struct {
	Items  []BookPublicPostDTO `json:"items"`
	Total  int64               `json:"total"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
}

// listRelatedBookPosts godoc
// @Summary 列出作品相关文章
// @Tags books-catalog
// @Produce json
// @Param workId path string true "作品 UUID"
// @Success 200 {object} BookPostListResult
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/works/{workId}/posts [get]
func (h *Handler) listRelatedBookPosts(c *gin.Context) {
	workID, ok := parseBookPathID(c, "workId")
	if !ok {
		return
	}
	limit, offset := bookPagination(c)
	items, total, err := h.service.ListRelatedBookPosts(c.Request.Context(), workID, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, BookPostListResult{Items: items, Total: total, Limit: limit, Offset: offset})
}

// linkBookPost godoc
// @Summary 关联自己的公开 Blog 文章
// @Tags books-contributions
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Param postId path string true "文章 UUID"
// @Success 200 {object} map[string]boolean
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/works/{workId}/posts/{postId} [put]
func (h *Handler) linkBookPost(c *gin.Context) {
	user, workID, postID, ok := bookPostRouteUser(c)
	if !ok {
		return
	}
	if err := h.service.LinkBookPost(user, workID, postID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"linked": true})
}

// unlinkBookPost godoc
// @Summary 取消 Blog 文章关联
// @Tags books-contributions
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Param postId path string true "文章 UUID"
// @Success 200 {object} map[string]boolean
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/works/{workId}/posts/{postId} [delete]
func (h *Handler) unlinkBookPost(c *gin.Context) {
	user, workID, postID, ok := bookPostRouteUser(c)
	if !ok {
		return
	}
	if err := h.service.UnlinkBookPost(user, workID, postID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"unlinked": true})
}

func bookPostRouteUser(c *gin.Context) (authctx.CurrentUser, uuid.UUID, uuid.UUID, bool) {
	user, ok := currentBookUser(c)
	if !ok {
		return authctx.CurrentUser{}, uuid.Nil, uuid.Nil, false
	}
	workID, ok := parseBookPathID(c, "workId")
	if !ok {
		return authctx.CurrentUser{}, uuid.Nil, uuid.Nil, false
	}
	postID, ok := parseBookPathID(c, "postId")
	if !ok {
		return authctx.CurrentUser{}, uuid.Nil, uuid.Nil, false
	}
	return user, workID, postID, true
}

func parseBookPathID(c *gin.Context, param string) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param(param)))
	if err != nil || id == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", param+" must be a valid UUID"))
		return uuid.Nil, false
	}
	return id, true
}
