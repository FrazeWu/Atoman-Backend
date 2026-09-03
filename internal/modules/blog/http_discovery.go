package blog

import (
	"errors"
	"net/http"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// searchBlogPosts godoc
// @Summary 搜索公开博客文章
// @Tags blog
// @Produce json
// @Param q query string true "关键词"
// @Param author_id query string false "作者 UUID"
// @Param channel_id query string false "频道 UUID"
// @Param collection_id query string false "合集 UUID"
// @Param tag query string false "标签"
// @Param sort query string false "排序" Enums(relevance,recent)
// @Param page query int false "页码"
// @Param page_size query int false "每页数量"
// @Success 200 {array} BlogSearchResultDTO
// @Router /api/v1/blog/search [get]
func (h *Handler) searchBlogPosts(c *gin.Context) {
	authorID, err := parseOptionalUUID(c.Query("author_id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "author_id must be a valid UUID"))
		return
	}
	channelID, err := parseOptionalUUID(c.Query("channel_id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "channel_id must be a valid UUID"))
		return
	}
	collectionID, err := parseOptionalUUID(c.Query("collection_id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "collection_id must be a valid UUID"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	items, total, err := h.service.SearchPublishedBlogContents(BlogSearchQuery{
		Text: c.Query("q"), Tag: c.Query("tag"), AuthorID: authorID, ChannelID: channelID, CollectionID: collectionID,
		Sort: c.DefaultQuery("sort", blogSearchSortRelevance), Page: page, PageSize: pageSize,
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, page, pageSize, total)
}

// createBlogRecommendationFeedback godoc
// @Summary 隐藏一篇博客推荐
// @Tags blog
// @Accept json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body blogRecommendationFeedbackInput true "推荐反馈"
// @Success 204
// @Router /api/v1/blog/recommendation-feedback [post]
func (h *Handler) createBlogRecommendationFeedback(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var input blogRecommendationFeedbackInput
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	if input.ContentID == uuid.Nil || strings.TrimSpace(strings.ToLower(input.Action)) != "hide" {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "content_id and hide action are required"))
		return
	}
	content, err := loadCanonicalBlogContent(h.service.db, input.ContentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	allowed, err := CanViewPublishedBlogContent(h.service.db, &user.ID, content)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if content.Status != "published" || !allowed {
		httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
		return
	}
	if err := h.service.HideBlogRecommendation(user.ID, input.ContentID); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// deleteBlogRecommendationFeedback godoc
// @Summary 恢复被隐藏的博客推荐
// @Tags blog
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "文章 UUID"
// @Success 204
// @Router /api/v1/blog/recommendation-feedback/{id} [delete]
func (h *Handler) deleteBlogRecommendationFeedback(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	contentID, err := parsePostID(c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.RestoreBlogRecommendation(user.ID, contentID); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// getBlogDigest godoc
// @Summary 获取订阅博客摘要
// @Tags blog
// @Security BearerAuth
// @Security CookieAuth
// @Param period query string false "摘要周期" Enums(day,week)
// @Success 200 {object} BlogDigestDTO
// @Router /api/v1/blog/digest [get]
func (h *Handler) getBlogDigest(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	result, err := h.service.BlogDigest(user.ID, c.DefaultQuery("period", blogDigestPeriodWeek))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
