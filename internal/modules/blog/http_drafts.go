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
	var posts []model.Post
	if err := h.service.db.Preload("Collection").Where("user_id = ? AND status = ?", user.ID, "draft").Order("updated_at DESC").Find(&posts).Error; err != nil {
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
	var draft model.BlogDraft
	if err := h.service.db.Where("user_id = ? AND context_key = ?", user.ID, contextKey).First(&draft).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.draft_not_found", "Draft not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildBlogDraftResponse(draft))
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
	draft := model.BlogDraft{
		UserID:       user.ID,
		ContextKey:   contextKey,
		SourcePostID: sourcePostID,
		Title:        req.Title,
		Content:      req.Content,
		Summary:      req.Summary,
		CoverURL:     req.CoverURL,
		Visibility:   normalizeBlogVisibility(req.Visibility),
		ChannelID:    channelID,
		CollectionID: collectionID,
	}
	if err := h.service.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "context_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"source_post_id", "title", "content", "summary", "cover_url", "visibility", "channel_id", "collection_id", "updated_at"}),
	}).Create(&draft).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildBlogDraftResponse(draft))
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
	if err := h.service.db.Where("user_id = ? AND context_key = ?", user.ID, contextKey).Delete(&model.BlogDraft{}).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func buildBlogDraftResponse(draft model.BlogDraft) blogDraftResponse {
	var sourcePostID *string
	if draft.SourcePostID != nil {
		value := draft.SourcePostID.String()
		sourcePostID = &value
	}

	var channelID *string
	if draft.ChannelID != nil {
		value := draft.ChannelID.String()
		channelID = &value
	}

	var collectionID *string
	if draft.CollectionID != nil {
		value := draft.CollectionID.String()
		collectionID = &value
	}

	return blogDraftResponse{
		ID:           draft.ID,
		UserID:       draft.UserID,
		ContextKey:   draft.ContextKey,
		SourcePostID: sourcePostID,
		Title:        draft.Title,
		Content:      draft.Content,
		Summary:      draft.Summary,
		CoverURL:     draft.CoverURL,
		Visibility:   draft.Visibility,
		ChannelID:    channelID,
		CollectionID: collectionID,
		CreatedAt:    draft.CreatedAt,
		UpdatedAt:    draft.UpdatedAt,
	}
}
