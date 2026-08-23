package blog

import (
	"errors"
	"net/http"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// getDrafts godoc
// @Summary 获取我的草稿文章
// @Description 返回当前登录用户的文章草稿列表。
// @Tags blog
// @Produce json
// @Success 200 {array} model.Post
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/posts/drafts [get]
func (h *Handler) getDrafts(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	query := canonicalBlogPostsQuery(h.service.db).
		Where("posts.author_id = ? AND posts.status = ?", user.ID, "draft").
		Order("posts.updated_at DESC, posts.id DESC")
	var rows []canonicalBlogPostRow
	if err := query.Find(&rows).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	posts, err := hydrateCanonicalBlogPosts(h.service.db, rows)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, posts)
}

func (h *Handler) getBlogDraft(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	contextKey := strings.TrimSpace(c.Query("context_key"))
	if contextKey == "" {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "context_key required"))
		return
	}
	var draft model.ContentBlogDraft
	if err := h.service.db.Where("user_id = ? AND context_key = ?", user.ID, contextKey).First(&draft).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.draft_not_found", "Draft not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildBlogDraftResponseFromCanonical(draft))
}

func (h *Handler) putBlogDraft(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req blogDraftInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	contextKey := strings.TrimSpace(req.ContextKey)
	if contextKey == "" {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "context_key required"))
		return
	}
	sourcePostID, err := parseOptionalUUID(req.SourcePostID)
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid source_post_id"))
		return
	}
	channelID, err := parseOptionalUUID(req.ChannelID)
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid channel_id"))
		return
	}
	collectionID, err := parseOptionalUUID(req.CollectionID)
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid collection_id"))
		return
	}
	if err := h.service.db.Transaction(func(tx *gorm.DB) error {
		var contentID *uuid.UUID
		if sourcePostID != nil {
			post, err := loadCanonicalBlogPost(tx, *sourcePostID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return apperr.NotFound("blog.post_not_found", "Post not found")
				}
				return err
			}
			contentID = &post.ID
		}
		if collectionID != nil {
			if _, err := canonicalBlogCollectionID(tx, *collectionID); err != nil {
				return err
			}
		}
		draft := model.ContentBlogDraft{
			UserID:       user.ID,
			ContextKey:   contextKey,
			ContentID:    contentID,
			Title:        req.Title,
			Content:      req.Content,
			Summary:      req.Summary,
			CoverURL:     req.CoverURL,
			Visibility:   normalizeBlogVisibility(req.Visibility),
			ChannelID:    channelID,
			CollectionID: collectionID,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "context_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"content_id", "title", "content", "summary", "cover_url", "visibility", "channel_id", "collection_id", "updated_at"}),
		}).Create(&draft).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		httpx.Error(c, err)
		return
	}
	var draft model.ContentBlogDraft
	if err := h.service.db.Where("user_id = ? AND context_key = ?", user.ID, contextKey).First(&draft).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildBlogDraftResponseFromCanonical(draft))
}

func (h *Handler) deleteBlogDraft(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	contextKey := strings.TrimSpace(c.Query("context_key"))
	if contextKey == "" {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "context_key required"))
		return
	}
	if err := h.service.db.Where("user_id = ? AND context_key = ?", user.ID, contextKey).Delete(&model.ContentBlogDraft{}).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}
