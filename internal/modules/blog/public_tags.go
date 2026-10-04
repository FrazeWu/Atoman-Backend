package blog

import (
	"net/http"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	maxPublicBlogTagsPerPost = 20
	maxPublicBlogTagsPerHour = 20
)

type publicBlogTagInput struct {
	Name string `json:"name" binding:"required"`
}

type PublicBlogTagDTO struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Count       int64     `json:"count"`
	ViewerAdded bool      `json:"viewer_added"`
	CreatedAt   time.Time `json:"created_at"`
}

type publicBlogTagCount struct {
	Name  string `gorm:"column:name"`
	Count int64  `gorm:"column:count"`
}

// getPublicBlogTags godoc
// @Summary 获取文章公共标签
// @Tags blog
// @Produce json
// @Param id path string true "文章 UUID"
// @Success 200 {array} PublicBlogTagDTO
// @Router /api/v1/blog/posts/{id}/public-tags [get]
func (h *Handler) getPublicBlogTags(c *gin.Context) {
	content, viewerID, ok := h.loadPublicTagContent(c)
	if !ok {
		return
	}
	var counts []publicBlogTagCount
	if err := h.service.db.Model(&model.ContentBlogUserTag{}).
		Select("name, COUNT(*) AS count").Where("content_id = ?", content.ID).
		Group("name").Order("count DESC, name ASC").Find(&counts).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	var viewerTags []model.ContentBlogUserTag
	if viewerID != nil {
		h.service.db.Where("content_id = ? AND user_id = ?", content.ID, *viewerID).Find(&viewerTags)
	}
	viewerAdded := make(map[string]bool, len(viewerTags))
	for _, tag := range viewerTags {
		viewerAdded[tag.Name] = true
	}
	var rows []model.ContentBlogUserTag
	if err := h.service.db.Where("content_id = ?", content.ID).Order("created_at ASC").Find(&rows).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	first := make(map[string]model.ContentBlogUserTag, len(rows))
	for _, row := range rows {
		if _, exists := first[row.Name]; !exists {
			first[row.Name] = row
		}
	}
	items := make([]PublicBlogTagDTO, 0, len(counts))
	for _, count := range counts {
		row := first[count.Name]
		items = append(items, PublicBlogTagDTO{ID: row.ID, Name: count.Name, Count: count.Count, ViewerAdded: viewerAdded[count.Name], CreatedAt: row.CreatedAt})
	}
	httpx.OK(c, http.StatusOK, items)
}

// createPublicBlogTag godoc
// @Summary 为文章添加公共标签
// @Tags blog
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "文章 UUID"
// @Param input body publicBlogTagInput true "公共标签"
// @Success 201 {object} PublicBlogTagDTO
// @Router /api/v1/blog/posts/{id}/public-tags [post]
func (h *Handler) createPublicBlogTag(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	content, _, loaded := h.loadPublicTagContent(c)
	if !loaded {
		return
	}
	var input publicBlogTagInput
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	name := normalizePublicBlogTag(input.Name)
	if name == "" || len([]rune(name)) > 48 {
		httpx.Error(c, apperr.BadRequest("blog.invalid_public_tag", "tag must be between 1 and 48 characters"))
		return
	}
	var total int64
	if err := h.service.db.Model(&model.ContentBlogUserTag{}).Where("content_id = ?", content.ID).Count(&total).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	if total >= maxPublicBlogTagsPerPost {
		httpx.Error(c, apperr.Conflict("blog.public_tags_limit", "this post already has the maximum number of public tags"))
		return
	}
	var recent int64
	if err := h.service.db.Model(&model.ContentBlogUserTag{}).Where("user_id = ? AND created_at >= ?", user.ID, time.Now().UTC().Add(-time.Hour)).Count(&recent).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	if recent >= maxPublicBlogTagsPerHour {
		httpx.Error(c, apperr.New(http.StatusTooManyRequests, "blog.public_tags_rate_limited", "too many public tags added recently", nil))
		return
	}
	var existing model.ContentBlogUserTag
	if err := h.service.db.Where("content_id = ? AND user_id = ? AND name = ?", content.ID, user.ID, name).First(&existing).Error; err == nil {
		httpx.Error(c, apperr.Conflict("blog.public_tag_exists", "you already added this tag"))
		return
	} else if err != nil && err != gorm.ErrRecordNotFound {
		httpx.Error(c, err)
		return
	}
	row := model.ContentBlogUserTag{ContentID: content.ID, UserID: user.ID, Name: name}
	if err := h.service.db.Create(&row).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, PublicBlogTagDTO{ID: row.ID, Name: row.Name, Count: 1, ViewerAdded: true, CreatedAt: row.CreatedAt})
}

// deletePublicBlogTag godoc
// @Summary 撤回文章公共标签
// @Tags blog
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "文章 UUID"
// @Param tag_id path string true "标签 UUID"
// @Success 204
// @Router /api/v1/blog/posts/{id}/public-tags/{tag_id} [delete]
func (h *Handler) deletePublicBlogTag(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	content, _, loaded := h.loadPublicTagContent(c)
	if !loaded {
		return
	}
	tagID, err := uuid.Parse(c.Param("tag_id"))
	if err != nil || tagID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "tag_id must be a valid UUID"))
		return
	}
	query := h.service.db.Where("id = ? AND content_id = ?", tagID, content.ID)
	if content.UserID != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		query = query.Where("user_id = ?", user.ID)
	}
	result := query.Delete(&model.ContentBlogUserTag{})
	if result.Error != nil {
		httpx.Error(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		httpx.Error(c, apperr.NotFound("blog.public_tag_not_found", "public tag not found"))
		return
	}
	c.Status(http.StatusNoContent)
}

func normalizePublicBlogTag(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func (h *Handler) loadPublicTagContent(c *gin.Context) (BlogContent, *uuid.UUID, bool) {
	contentID, err := parsePostID(c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return BlogContent{}, nil, false
	}
	content, err := loadCanonicalBlogContent(h.service.db, contentID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
		} else {
			httpx.Error(c, err)
		}
		return BlogContent{}, nil, false
	}
	viewerID := currentViewerID(c)
	allowed, err := CanViewPublishedBlogContent(h.service.db, viewerID, content)
	if err != nil {
		httpx.Error(c, err)
		return BlogContent{}, nil, false
	}
	if content.Status != "published" || !allowed {
		httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
		return BlogContent{}, nil, false
	}
	return content, viewerID, true
}
