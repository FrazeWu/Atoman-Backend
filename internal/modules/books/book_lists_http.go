package books

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func bookListRouteUser(c *gin.Context) (authctx.CurrentUser, uuid.UUID, bool) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return user, uuid.Nil, false
	}
	id, ok := parseCatalogID(c, "listId")
	return user, id, ok
}

// searchBookLists godoc
// @Summary 搜索已公开书单的发布标题和简介，不搜索私人草稿
// @Tags books-lists
// @Produce json
// @Param q query string false "公开标题或简介"
// @Param limit query int false "每页数量，最多 100"
// @Param offset query int false "偏移量"
// @Success 200 {object} BookListSearchResult
// @Router /api/v1/books/booklists [get]
func (h *Handler) searchBookLists(c *gin.Context) {
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	result, err := h.service.SearchBookLists(c.Request.Context(), c.Query("q"), limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// getBookList godoc
// @Summary 读取书单的发布内容；私人书单仅本人可读
// @Description 不返回草稿、私人编辑版本或私人编辑时间。不可用作品保留原 ID 和推荐语，并标记 available=false。
// @Tags books-lists
// @Produce json
// @Param listId path string true "书单 UUID"
// @Success 200 {object} BookListDetail
// @Router /api/v1/books/booklists/{listId} [get]
func (h *Handler) getBookList(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	id, ok := parseCatalogID(c, "listId")
	if !ok {
		return
	}
	user, _ := authctx.Current(c)
	result, err := h.service.GetBookList(c.Request.Context(), user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// listMyBookLists godoc
// @Summary 分页列出本人的书单草稿
// @Tags books-lists
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param limit query int false "每页数量，最多 100"
// @Param offset query int false "偏移量"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/books/me/booklists [get]
func (h *Handler) listMyBookLists(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	items, total, err := h.service.ListMyBookLists(c.Request.Context(), user, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}

// getBookListDraft godoc
// @Summary 读取本人的完整书单草稿和并发版本
// @Tags books-lists
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param listId path string true "书单 UUID"
// @Success 200 {object} BookListDraftDTO
// @Router /api/v1/books/me/booklists/{listId}/draft [get]
func (h *Handler) getBookListDraft(c *gin.Context) {
	user, id, ok := bookListRouteUser(c)
	if !ok {
		return
	}
	result, err := h.service.GetBookListDraft(c.Request.Context(), user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// saveBookListDraft godoc
// @Summary 创建或替换私人书单草稿，按数组顺序保存作品及推荐语
// @Description 首次创建使用客户端 UUID 和 base_version=0，更新需当前版本。最多 200 本，不重复收录合并别名。不影响已发布内容。
// @Tags books-lists
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param listId path string true "客户端生成的书单 UUID"
// @Param input body SaveBookListDraftInput true "完整草稿"
// @Success 200 {object} BookListDraftDTO
// @Router /api/v1/books/me/booklists/{listId}/draft [put]
func (h *Handler) saveBookListDraft(c *gin.Context) {
	user, id, ok := bookListRouteUser(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*1024*1024)
	var input SaveBookListDraftInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("books.invalid_list", "Invalid draft or body exceeds 2 MiB"))
		return
	}
	result, err := h.service.SaveBookListDraft(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// publishBookList godoc
// @Summary 按已确认草稿版本发布书单，明确选择公开或私人
// @Description 发布内容与私人草稿独立。相同版本重试不会重复发布；草稿编辑不会改变发现排序或收藏更新提示。
// @Tags books-lists
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param listId path string true "书单 UUID"
// @Param input body PublishBookListInput true "草稿版本和 public/private"
// @Success 200 {object} BookListDraftDTO
// @Router /api/v1/books/me/booklists/{listId}/publish [post]
func (h *Handler) publishBookList(c *gin.Context) {
	user, id, ok := bookListRouteUser(c)
	if !ok {
		return
	}
	var input PublishBookListInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("books.invalid_list_publication", "Invalid publication request"))
		return
	}
	result, err := h.service.PublishBookList(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// unpublishBookList godoc
// @Summary 撤下公开书单，保留本人草稿和发布内容
// @Tags books-lists
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param listId path string true "书单 UUID"
// @Param input body BookListVersionInput true "当前私人编辑版本"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/books/me/booklists/{listId}/unpublish [post]
func (h *Handler) unpublishBookList(c *gin.Context) { h.withdrawBookList(c, false) }

// deleteBookList godoc
// @Summary 删除本人书单；草稿、发现和收藏列表均不再返回
// @Tags books-lists
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param listId path string true "书单 UUID"
// @Param base_version query int true "当前私人编辑版本"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/books/me/booklists/{listId} [delete]
func (h *Handler) deleteBookList(c *gin.Context) { h.withdrawBookList(c, true) }

func (h *Handler) withdrawBookList(c *gin.Context, remove bool) {
	user, id, ok := bookListRouteUser(c)
	if !ok {
		return
	}
	var version int64
	if remove {
		version, ok = bookPrivateBaseVersion(c)
		if !ok {
			return
		}
	} else {
		var input BookListVersionInput
		if err := c.ShouldBindJSON(&input); err != nil {
			httpx.Error(c, apperr.BadRequest("books.invalid_list", "Invalid withdrawal request"))
			return
		}
		version = input.BaseVersion
	}
	if err := h.service.WithdrawBookList(c.Request.Context(), user, id, version, remove); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"version": version + 1, "deleted": remove})
}

// saveBookListBookmark godoc
// @Summary 收藏书单并可明确确认已读的发布版本
// @Description seen_version 省略时不标记已读，旧请求不会倒退已读版本；未来版本无效。收藏不会授权访问私人或撤下的书单。
// @Tags books-lists
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param listId path string true "书单 UUID"
// @Param input body SaveBookListBookmarkInput true "可选已读发布版本"
// @Success 200 {object} BookListBookmarkDTO
// @Router /api/v1/books/booklists/{listId}/bookmark [put]
func (h *Handler) saveBookListBookmark(c *gin.Context) {
	user, id, ok := bookListRouteUser(c)
	if !ok {
		return
	}
	var input SaveBookListBookmarkInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("books.invalid_list_bookmark", "Invalid bookmark request"))
		return
	}
	result, err := h.service.SaveBookListBookmark(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// deleteBookListBookmark godoc
// @Summary 移除本人的书单收藏；书单撤下后仍可移除
// @Tags books-lists
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param listId path string true "书单 UUID"
// @Success 200 {object} map[string]bool
// @Router /api/v1/books/booklists/{listId}/bookmark [delete]
func (h *Handler) deleteBookListBookmark(c *gin.Context) {
	user, id, ok := bookListRouteUser(c)
	if !ok {
		return
	}
	if err := h.service.DeleteBookListBookmark(c.Request.Context(), user, id); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"removed": true})
}

// listBookListBookmarks godoc
// @Summary 列出本人仍可访问的书单收藏及发布更新摘要
// @Tags books-lists
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param limit query int false "每页数量，最多 100"
// @Param offset query int false "偏移量"
// @Success 200 {object} BookListBookmarkResult
// @Router /api/v1/books/me/booklist-bookmarks [get]
func (h *Handler) listBookListBookmarks(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	result, err := h.service.ListBookListBookmarks(c.Request.Context(), user, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
