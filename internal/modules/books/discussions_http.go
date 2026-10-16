package books

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// createBookDiscussionPassage godoc
// @Summary 创建固定文件修订的公开段落讨论定位
// @Description 相同修订和相同选区共享讨论。只保存公开选区，不复制私人标注、摘录或笔记。使用返回的 comments_path 访问统一评论接口。
// @Tags books-discussions
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param revisionId path string true "文件修订 UUID"
// @Param input body CreateBookDiscussionPassageInput true "对应文件格式的选区"
// @Success 200 {object} BookDiscussionPassageDTO
// @Router /api/v1/books/asset-revisions/{revisionId}/passages [post]
func (h *Handler) createBookDiscussionPassage(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128*1024)
	var input CreateBookDiscussionPassageInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid public passage selection"))
		return
	}
	result, err := h.service.CreateBookDiscussionPassage(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// getBookDiscussionPassage godoc
// @Summary 获取可公开阅读文件中的段落讨论定位
// @Tags books-discussions
// @Produce json
// @Param passageId path string true "段落讨论 UUID"
// @Success 200 {object} BookDiscussionPassageDTO
// @Router /api/v1/books/passages/{passageId} [get]
func (h *Handler) getBookDiscussionPassage(c *gin.Context) {
	id, ok := parseCatalogID(c, "passageId")
	if !ok {
		return
	}
	result, err := h.service.GetBookDiscussionPassage(c.Request.Context(), id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// listBookDiscussionPassages godoc
// @Summary 按固定文件修订列出公开段落讨论定位
// @Tags books-discussions
// @Produce json
// @Param revisionId path string true "文件修订 UUID"
// @Param limit query int false "每页数量，最多 100"
// @Param offset query int false "偏移量"
// @Success 200 {object} BookDiscussionPassageList
// @Router /api/v1/books/asset-revisions/{revisionId}/passages [get]
func (h *Handler) listBookDiscussionPassages(c *gin.Context) {
	id, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	result, err := h.service.ListBookDiscussionPassages(c.Request.Context(), id, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// linkBookContentReview godoc
// @Summary 将本人的统一博客内容关联为图书长评
// @Description 先使用博客接口创建或编辑内容，再关联。草稿或私有内容不会被此操作公开；发布后自动进入图书公开长评列表。正文和评论只使用原内容 UUID。
// @Tags books-reviews
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Param contentId path string true "统一博客内容 UUID"
// @Param input body LinkBookContentReviewInput true "剧透标记"
// @Success 200 {object} model.BookContentReview
// @Router /api/v1/books/works/{workId}/reviews/{contentId} [put]
func (h *Handler) linkBookContentReview(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	workID, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	contentID, ok := parseCatalogID(c, "contentId")
	if !ok {
		return
	}
	var input LinkBookContentReviewInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid review relation"))
		return
	}
	result, err := h.service.LinkBookContentReview(c.Request.Context(), user, workID, contentID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// unlinkBookContentReview godoc
// @Summary 取消本人的图书长评关联，不删除原内容或评论
// @Tags books-reviews
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Param contentId path string true "统一博客内容 UUID"
// @Success 200 {object} map[string]bool
// @Router /api/v1/books/works/{workId}/reviews/{contentId} [delete]
func (h *Handler) unlinkBookContentReview(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	workID, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	contentID, ok := parseCatalogID(c, "contentId")
	if !ok {
		return
	}
	if err := h.service.UnlinkBookContentReview(c.Request.Context(), user, workID, contentID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"unlinked": true})
}

// listBookContentReviews godoc
// @Summary 列出作品及其合并别名下已发布公开的统一内容长评
// @Tags books-reviews
// @Produce json
// @Param workId path string true "作品 UUID"
// @Param limit query int false "每页数量，最多 100"
// @Param offset query int false "偏移量"
// @Success 200 {object} BookContentReviewList
// @Router /api/v1/books/works/{workId}/reviews [get]
func (h *Handler) listBookContentReviews(c *gin.Context) {
	workID, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	result, err := h.service.ListBookContentReviews(c.Request.Context(), workID, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
