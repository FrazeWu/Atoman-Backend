package blog

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Handler struct {
	service *Service
}

type setRatingRequest struct {
	Score int `json:"score"`
}

type channelInput struct {
	Name        string `json:"name" binding:"required"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
}

type collectionInput struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
}

type bookmarkInput struct {
	PostID           uuid.UUID  `json:"post_id" binding:"required"`
	BookmarkFolderID *uuid.UUID `json:"bookmark_folder_id"`
}

type bookmarkFolderInput struct {
	Name string `json:"name" binding:"required"`
}

type postInput struct {
	Title         string   `json:"title" binding:"required"`
	Content       string   `json:"content" binding:"required"`
	Summary       string   `json:"summary"`
	CoverURL      string   `json:"cover_url"`
	Visibility    string   `json:"visibility"`
	AllowComments *bool    `json:"allow_comments"`
	Status        string   `json:"status"`
	ChannelID     *string  `json:"channel_id"`
	CollectionIDs []string `json:"collection_ids"`
}

type collectionActionInput struct {
	CollectionID uuid.UUID `json:"collection_id" binding:"required"`
}

type reorderCollectionPostsInput struct {
	PostIDs []string `json:"post_ids"`
}

type blogDraftInput struct {
	ContextKey    string   `json:"context_key" binding:"required"`
	SourcePostID  string   `json:"source_post_id"`
	Title         string   `json:"title"`
	Content       string   `json:"content"`
	Summary       string   `json:"summary"`
	CoverURL      string   `json:"cover_url"`
	Visibility    string   `json:"visibility"`
	AllowComments *bool    `json:"allow_comments"`
	ChannelID     string   `json:"channel_id"`
	CollectionIDs []string `json:"collection_ids"`
}

type blogDraftResponse struct {
	ID            uuid.UUID `json:"id"`
	UserID        uuid.UUID `json:"user_id"`
	ContextKey    string    `json:"context_key"`
	SourcePostID  *string   `json:"source_post_id,omitempty"`
	Title         string    `json:"title"`
	Content       string    `json:"content"`
	Summary       string    `json:"summary"`
	CoverURL      string    `json:"cover_url"`
	Visibility    string    `json:"visibility"`
	AllowComments bool      `json:"allow_comments"`
	ChannelID     *string   `json:"channel_id,omitempty"`
	CollectionIDs []string  `json:"collection_ids"`
	CreatedAt     any       `json:"created_at"`
	UpdatedAt     any       `json:"updated_at"`
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.GET("/channels", h.listChannels)
	group.GET("/channels/:id", h.getChannel)
	group.GET("/channels/:id/collections", h.getChannelCollections)
	group.GET("/channels/slug/:slug", h.getChannelBySlug)
	group.GET("/channels/slug/:slug/collections", h.getChannelCollectionsBySlug)
	group.GET("/channels/slug/:slug/rss/article", h.getChannelArticleRSS)
	group.GET("/collections", h.listUserCollections)
	group.GET("/collections/:id", h.getCollection)
	group.POST("/channels/ensure-default", h.ensureDefaultChannel)
	group.POST("/channels", h.createChannel)
	group.PUT("/channels/:id", h.updateChannel)
	group.DELETE("/channels/:id", h.deleteChannel)
	group.POST("/channels/:id/collections", h.createCollection)
	group.PUT("/collections/:id", h.updateCollection)
	group.DELETE("/collections/:id", h.deleteCollection)
	group.GET("/posts/:id/likes/count", h.getPostLikesCount)
	group.GET("/posts/:id/comments", h.listComments)
	group.POST("/posts/:id/comments", h.createComment)
	group.DELETE("/comments/:id", h.deleteComment)
	group.POST("/likes", h.createLike)
	group.DELETE("/likes", h.deleteLike)
	group.GET("/bookmarks", h.listBookmarks)
	group.POST("/bookmarks", h.createBookmark)
	group.DELETE("/bookmarks/:id", h.deleteBookmark)
	group.GET("/bookmark-folders", h.listBookmarkFolders)
	group.POST("/bookmark-folders", h.createBookmarkFolder)
	group.DELETE("/bookmark-folders/:id", h.deleteBookmarkFolder)
	group.GET("/posts", h.listPosts)
	group.GET("/recommend/posts", h.listRecommendedPosts)
	group.GET("/posts/drafts", h.getDrafts)
	group.GET("/posts/:id", h.getPost)
	group.POST("/posts", h.createPost)
	group.PUT("/posts/:id", h.updatePost)
	group.DELETE("/posts/:id", h.deletePost)
	group.POST("/posts/:id/publish", h.publishPost)
	group.POST("/posts/:id/unpublish", h.unpublishPost)
	group.POST("/posts/:id/pin", h.pinPost)
	group.POST("/posts/:id/unpin", h.unpinPost)
	group.POST("/posts/:id/collections", h.addPostToCollection)
	group.DELETE("/posts/:id/collections/:collection_id", h.removePostFromCollection)
	group.PUT("/collections/:id/posts/order", h.reorderCollectionPosts)
	group.GET("/drafts", h.getBlogDraft)
	group.PUT("/drafts", h.putBlogDraft)
	group.DELETE("/drafts", h.deleteBlogDraft)
	group.PUT("/posts/:id/rating", h.setRating)
}

func (h *Handler) listChannels(c *gin.Context) {
	var userID *uuid.UUID
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "user_id must be a valid uuid"))
			return
		}
		userID = &parsed
	}
	channels, err := h.service.ListChannels(userID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channels)
}

func (h *Handler) getChannel(c *gin.Context) {
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	channel, err := h.service.GetChannel(channelID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channel)
}

func (h *Handler) getChannelCollections(c *gin.Context) {
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	collections, err := h.service.ListCollectionsByChannel(channelID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collections)
}

func (h *Handler) getChannelBySlug(c *gin.Context) {
	channel, err := h.service.GetChannelBySlug(c.Param("slug"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channel)
}

func (h *Handler) getChannelCollectionsBySlug(c *gin.Context) {
	_, collections, err := h.service.ListCollectionsByChannelSlug(c.Param("slug"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collections)
}

func (h *Handler) getCollection(c *gin.Context) {
	collectionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	collection, err := h.service.GetCollection(collectionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collection)
}

func (h *Handler) getChannelArticleRSS(c *gin.Context) {
	channel, err := h.service.GetChannelBySlug(c.Param("slug"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var posts []model.Post
	if err := h.service.db.Where("channel_id = ? AND status = ?", channel.ID, "published").
		Preload("User").
		Order("created_at DESC").
		Limit(50).
		Find(&posts).Error; err != nil {
		httpx.Error(c, err)
		return
	}

	scheme := c.Request.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "https"
	}
	siteURL := fmt.Sprintf("%s://%s", scheme, c.Request.Host)

	c.Header("Content-Type", "application/rss+xml; charset=utf-8")
	c.String(http.StatusOK, buildArticleRSS(channel, posts, siteURL))
}

func (h *Handler) listUserCollections(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	collections, err := h.service.ListUserCollections(user.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collections)
}

func (h *Handler) ensureDefaultChannel(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	channel, err := h.service.CreateDefaultChannelForUser(user.ID, user.Username)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channel)
}

func (h *Handler) createChannel(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req channelInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	channel, err := h.service.CreateChannel(user, req.Name, req.Slug, req.Description, req.CoverURL)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, channel)
}

func (h *Handler) updateChannel(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	var req channelInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	channel, err := h.service.UpdateChannel(user, channelID, req.Name, req.Slug, req.Description, req.CoverURL)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channel)
}

func (h *Handler) deleteChannel(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	if err := h.service.DeleteChannel(user, channelID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Channel deleted"})
}

func (h *Handler) createCollection(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	var req collectionInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	collection, err := h.service.CreateCollection(user, channelID, req.Name, req.Description, req.CoverURL)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, collection)
}

func (h *Handler) updateCollection(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	collectionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	var req collectionInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	collection, err := h.service.UpdateCollection(user, collectionID, req.Name, req.Description, req.CoverURL)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collection)
}

func (h *Handler) deleteCollection(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	collectionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	if err := h.service.DeleteCollection(user, collectionID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Collection deleted"})
}

func (h *Handler) getPostLikesCount(c *gin.Context) {
	postID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	count, err := h.service.CountPostLikes(postID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"count": count})
}

func (h *Handler) listComments(c *gin.Context) {
	postID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	var viewerID *uuid.UUID
	if user, ok := authctx.Current(c); ok && user.ID != uuid.Nil {
		viewerID = &user.ID
	}
	comments, err := h.service.ListComments(postID, viewerID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, comments)
}

func (h *Handler) createComment(c *gin.Context) {
	postID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	var req struct {
		GuestName    string `json:"guest_name"`
		Content      string `json:"content"`
		TimestampSec *int   `json:"timestamp_sec"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	var current *authctx.CurrentUser
	if user, ok := authctx.Current(c); ok {
		current = &user
	}
	comment, err := h.service.CreateComment(current, postID, req.GuestName, req.Content, req.TimestampSec)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, comment)
}

func (h *Handler) deleteComment(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	commentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	if err := h.service.DeleteComment(user, commentID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func (h *Handler) createLike(c *gin.Context) {
	h.toggleLike(c, true)
}

func (h *Handler) deleteLike(c *gin.Context) {
	h.toggleLike(c, false)
}

func (h *Handler) toggleLike(c *gin.Context, isLike bool) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	targetID, err := uuid.Parse(req.TargetID)
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "target_id must be a valid uuid"))
		return
	}
	if err := h.service.ToggleLike(user, req.TargetType, targetID, isLike); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func (h *Handler) listBookmarks(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var folderID *uuid.UUID
	if raw := strings.TrimSpace(c.Query("folder_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "folder_id must be a valid uuid"))
			return
		}
		folderID = &parsed
	}
	sort := strings.TrimSpace(c.DefaultQuery("sort", "latest"))
	bookmarks, err := h.service.ListBookmarks(user, folderID, sort)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, bookmarks)
}

func (h *Handler) createBookmark(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req bookmarkInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	bookmark, err := h.service.CreateBookmark(user, req.PostID, req.BookmarkFolderID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, bookmark)
}

func (h *Handler) deleteBookmark(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	bookmarkID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	if err := h.service.DeleteBookmark(user, bookmarkID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func (h *Handler) listBookmarkFolders(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	folders, err := h.service.ListBookmarkFolders(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, folders)
}

func (h *Handler) createBookmarkFolder(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req bookmarkFolderInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	folder, err := h.service.CreateBookmarkFolder(user, req.Name)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, folder)
}

func (h *Handler) deleteBookmarkFolder(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	folderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	if err := h.service.DeleteBookmarkFolder(user, folderID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func buildArticleRSS(ch model.Channel, posts []model.Post, siteURL string) string {
	var items strings.Builder
	for _, p := range posts {
		pubDate := p.CreatedAt.Format(time.RFC1123Z)
		summary := p.Summary
		if summary == "" && len(p.Content) > 280 {
			summary = p.Content[:280] + "…"
		} else if summary == "" {
			summary = p.Content
		}
		authorName := ""
		if p.User != nil {
			authorName = p.User.DisplayName
			if authorName == "" {
				authorName = p.User.Username
			}
		}
		items.WriteString(fmt.Sprintf(`
    <item>
      <title><![CDATA[%s]]></title>
      <link>%s/post/%s</link>
      <guid isPermaLink="true">%s/post/%s</guid>
      <pubDate>%s</pubDate>
      <description><![CDATA[%s]]></description>
      <author>%s</author>
    </item>`, p.Title, siteURL, p.ID, siteURL, p.ID, pubDate, summary, authorName))
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title><![CDATA[%s]]></title>
    <link>%s/channel/%s</link>
    <description><![CDATA[%s]]></description>
    <language>zh-cn</language>
    <lastBuildDate>%s</lastBuildDate>
    %s
  </channel>
</rss>`, ch.Name, siteURL, ch.Slug, ch.Description,
		time.Now().Format(time.RFC1123Z), items.String())
}

// listPosts godoc
// @Summary 获取已发布文章列表
// @Description 返回已发布文章，可按用户、频道或合集筛选。
// @Tags blog
// @Produce json
// @Param user_id query string false "用户 UUID"
// @Param channel_id query string false "频道 UUID"
// @Param collection_id query string false "合集 UUID"
// @Param q query string false "搜索标题或摘要"
// @Param limit query int false "返回数量上限"
// @Success 200 {array} model.Post
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/blog/posts [get]
func (h *Handler) listPosts(c *gin.Context) {
	var posts []model.Post
	limit := boundedListLimit(c.Query("limit"), 0, 40)
	query := h.service.db.Preload("User").Preload("Channel").Preload("Collections").Where("status = ?", "published")

	if userID := c.Query("user_id"); userID != "" {
		query = query.Where("user_id = ?", userID)
	}
	if channelID := c.Query("channel_id"); channelID != "" {
		query = query.Where("channel_id = ?", channelID)
	}
	if collectionID := c.Query("collection_id"); collectionID != "" {
		query = query.Joins("JOIN post_collections pc ON pc.post_id = posts.id").Where("pc.collection_id = ?", collectionID)
		query = query.Order("pc.position ASC")
	} else {
		query = query.Order("pinned DESC, created_at DESC")
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		searchLike := "%" + q + "%"
		query = query.Where("(title ILIKE ? OR summary ILIKE ? OR content ILIKE ?)", searchLike, searchLike, searchLike)
	}
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Find(&posts).Error; err != nil {
		httpx.Error(c, err)
		return
	}

	viewerID := currentViewerID(c)
	visiblePosts := make([]model.Post, 0, len(posts))
	for _, post := range posts {
		allowed, err := canViewPublishedPost(h.service.db, viewerID, post)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		if allowed {
			visiblePosts = append(visiblePosts, post)
		}
	}

	httpx.OK(c, http.StatusOK, visiblePosts)
}

func (h *Handler) listRecommendedPosts(c *gin.Context) {
	mode, err := parseRecommendationMode(c.DefaultQuery("mode", "hot"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	page, pageSize := httpx.PageParams(c)
	items, total, err := h.service.RecommendPostsByMode(mode, page, pageSize)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, page, pageSize, total)
}

// getPost godoc
// @Summary 获取文章详情
// @Description 返回指定文章；若文章为草稿，则仅作者本人可查看。
// @Tags blog
// @Produce json
// @Param id path string true "文章 UUID"
// @Success 200 {object} model.Post
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/blog/posts/{id} [get]
func (h *Handler) getPost(c *gin.Context) {
	postID, err := parsePostID(c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var post model.Post
	if err := h.service.db.Preload("User").Preload("Channel").Preload("Collections").First(&post, "id = ?", postID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
			return
		}
		httpx.Error(c, err)
		return
	}

	viewerID := currentViewerID(c)
	if post.Status == "draft" {
		if viewerID == nil || post.UserID != *viewerID {
			httpx.Error(c, apperr.Forbidden("blog.post_forbidden", "You don't have permission to view this draft"))
			return
		}
	} else {
		allowed, err := canViewPublishedPost(h.service.db, viewerID, post)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		if !allowed {
			httpx.Error(c, apperr.Forbidden("blog.post_forbidden", "You don't have permission to view this post"))
			return
		}
	}

	likesCount, err := h.service.CountPostLikes(post.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}

	liked := false
	if viewerID != nil {
		var likedCount int64
		if err := h.service.db.Model(&model.Like{}).
			Where("user_id = ? AND target_type = ? AND target_id = ?", *viewerID, "post", post.ID).
			Count(&likedCount).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		liked = likedCount > 0
	}

	httpx.OK(c, http.StatusOK, struct {
		model.Post
		Liked      bool  `json:"liked"`
		LikesCount int64 `json:"likes_count"`
	}{
		Post:       post,
		Liked:      liked,
		LikesCount: likesCount,
	})
}

func currentViewerID(c *gin.Context) *uuid.UUID {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		return nil
	}
	return &user.ID
}

func canViewPublishedPost(db *gorm.DB, viewerID *uuid.UUID, post model.Post) (bool, error) {
	switch post.Visibility {
	case "", "public":
		return true, nil
	case "private":
		return viewerID != nil && post.UserID == *viewerID, nil
	case "followers":
		if viewerID == nil {
			return false, nil
		}
		if post.UserID == *viewerID {
			return true, nil
		}
		if post.ChannelID == nil {
			return false, nil
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte("internal_channel:"+post.ChannelID.String())))
		var source model.FeedSource
		if err := db.Where("hash = ?", hash).First(&source).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		var sub model.Subscription
		if err := db.Where("user_id = ? AND feed_source_id = ?", *viewerID, source.ID).First(&sub).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}
}

func boundedListLimit(raw string, fallback int, max int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	if max > 0 && value > max {
		return max
	}
	return value
}

// createPost godoc
// @Summary 创建文章
// @Description 使用模块化博客服务创建文章。
// @Tags blog
// @Accept json
// @Produce json
// @Param input body CreatePostRequest true "文章输入"
// @Success 201 {object} model.Post
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/posts [post]
func (h *Handler) createPost(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	var req CreatePostRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}

	post, err := h.service.CreatePost(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, post)
}

func (h *Handler) setRating(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	postID, err := parsePostID(c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req setRatingRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}

	summary, err := h.service.SetRating(user, postID, req.Score)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, summary)
}

func (h *Handler) updatePost(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	postID, err := parsePostID(c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req postInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}

	var post model.Post
	if err := h.service.db.First(&post, "id = ?", postID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	if post.UserID != user.ID {
		httpx.Error(c, apperr.Forbidden("blog.post_forbidden", "You don't have permission to update this post"))
		return
	}

	updates := map[string]any{
		"title":      req.Title,
		"content":    req.Content,
		"summary":    req.Summary,
		"cover_url":  req.CoverURL,
		"visibility": normalizeBlogVisibility(req.Visibility),
	}

	targetChannelID := post.ChannelID
	if req.ChannelID != nil {
		trimmedChannelID := strings.TrimSpace(*req.ChannelID)
		if trimmedChannelID == "" {
			targetChannelID = nil
			updates["channel_id"] = nil
		} else {
			parsedChannelID, err := uuid.Parse(trimmedChannelID)
			if err != nil {
				httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid channel UUID"))
				return
			}
			var channel model.Channel
			if err := h.service.db.First(&channel, "id = ?", parsedChannelID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					httpx.Error(c, apperr.NotFound("blog.channel_not_found", "Channel not found"))
					return
				}
				httpx.Error(c, err)
				return
			}
			if channel.UserID == nil || *channel.UserID != user.ID {
				httpx.Error(c, apperr.Forbidden("blog.channel_forbidden", "You don't have permission to move post to this channel"))
				return
			}
			targetChannelID = &parsedChannelID
			updates["channel_id"] = parsedChannelID
		}
	}

	selectedCollections := make([]model.Collection, 0, len(req.CollectionIDs))
	if req.CollectionIDs != nil {
		for _, collectionIDStr := range req.CollectionIDs {
			collectionID, err := uuid.Parse(collectionIDStr)
			if err != nil {
				httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid collection UUID"))
				return
			}
			var collection model.Collection
			if err := h.service.db.Preload("Channel").First(&collection, "id = ?", collectionID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					httpx.Error(c, apperr.NotFound("blog.collection_not_found", "Collection not found"))
					return
				}
				httpx.Error(c, err)
				return
			}
			if collection.Channel == nil || collection.Channel.UserID == nil || *collection.Channel.UserID != user.ID {
				httpx.Error(c, apperr.Forbidden("blog.collection_forbidden", "You don't have permission to assign this collection"))
				return
			}
			if targetChannelID == nil || collection.ChannelID != *targetChannelID {
				httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Collection does not belong to selected channel"))
				return
			}
			selectedCollections = append(selectedCollections, collection)
		}
	}

	if req.Status == "published" || req.Status == "draft" {
		updates["status"] = req.Status
	}
	if req.AllowComments != nil {
		updates["allow_comments"] = *req.AllowComments
	}

	shouldUpdateCollections := req.ChannelID != nil || req.CollectionIDs != nil
	if err := h.service.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&post).Updates(updates).Error; err != nil {
			return err
		}

		if shouldUpdateCollections {
			if targetChannelID != nil {
				defaultCollection, err := ensureDefaultCollection(tx, *targetChannelID)
				if err != nil {
					return err
				}
				collectionsToAssign := make([]model.Collection, 0, len(selectedCollections)+1)
				collectionsToAssign = append(collectionsToAssign, *defaultCollection)
				for _, collection := range selectedCollections {
					if collection.ID == defaultCollection.ID {
						continue
					}
					collectionsToAssign = append(collectionsToAssign, collection)
				}
				if err := tx.Model(&post).Association("Collections").Replace(collectionsToAssign); err != nil {
					return err
				}
			} else if err := tx.Model(&post).Association("Collections").Clear(); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		httpx.Error(c, err)
		return
	}

	if err := h.service.db.Preload("Channel").Preload("Collections").First(&post, "id = ?", post.ID).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, post)
}

func (h *Handler) deletePost(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	postID, err := parsePostID(c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var post model.Post
	if err := h.service.db.First(&post, "id = ?", postID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	if post.UserID != user.ID {
		httpx.Error(c, apperr.Forbidden("blog.post_forbidden", "You don't have permission to delete this post"))
		return
	}
	if err := h.service.db.Delete(&post).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func (h *Handler) publishPost(c *gin.Context) { h.updatePostStatus(c, "published") }

func (h *Handler) unpublishPost(c *gin.Context) { h.updatePostStatus(c, "draft") }

func (h *Handler) pinPost(c *gin.Context) { h.updatePostPin(c, true) }

func (h *Handler) unpinPost(c *gin.Context) { h.updatePostPin(c, false) }

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
	if err := h.service.db.Preload("Collections").Where("user_id = ? AND status = ?", user.ID, "draft").Order("updated_at DESC").Find(&posts).Error; err != nil {
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
	collectionIDs, err := normalizeUUIDList(req.CollectionIDs)
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid collection_ids"))
		return
	}
	allowComments := true
	if req.AllowComments != nil {
		allowComments = *req.AllowComments
	}
	draft := model.BlogDraft{
		UserID:        user.ID,
		ContextKey:    strings.TrimSpace(req.ContextKey),
		SourcePostID:  sourcePostID,
		Title:         req.Title,
		Content:       req.Content,
		Summary:       req.Summary,
		CoverURL:      req.CoverURL,
		Visibility:    normalizeBlogVisibility(req.Visibility),
		AllowComments: allowComments,
		ChannelID:     channelID,
		CollectionIDs: strings.Join(collectionIDs, ","),
	}
	if err := h.service.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "context_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"source_post_id", "title", "content", "summary", "cover_url", "visibility", "allow_comments", "channel_id", "collection_ids", "updated_at"}),
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

// addPostToCollection godoc
// @Summary 将文章加入合集
// @Description 为当前用户拥有的文章增加一个同频道合集归属。
// @Tags blog
// @Accept json
// @Produce json
// @Param id path string true "文章 UUID"
// @Param input body collectionActionInput true "合集操作输入"
// @Success 200 {object} handlers.MessageResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/posts/{id}/collections [post]
func (h *Handler) addPostToCollection(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	postID, err := parsePostID(c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req collectionActionInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	var post model.Post
	if err := h.service.db.First(&post, "id = ?", postID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	if post.UserID != user.ID {
		httpx.Error(c, apperr.Forbidden("blog.post_forbidden", "You don't have permission to modify this post"))
		return
	}
	if post.ChannelID == nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Post is not assigned to a channel"))
		return
	}
	var collection model.Collection
	if err := h.service.db.Preload("Channel").First(&collection, "id = ?", req.CollectionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.collection_not_found", "Collection not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	if collection.Channel == nil || collection.Channel.UserID == nil || *collection.Channel.UserID != user.ID {
		httpx.Error(c, apperr.Forbidden("blog.collection_forbidden", "You don't have permission to add to this collection"))
		return
	}
	if collection.ChannelID != *post.ChannelID {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Collection does not belong to post channel"))
		return
	}
	if err := h.service.db.Model(&post).Association("Collections").Append(&collection); err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.appendPostCollectionAtTail(post.ID, collection.ID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

// removePostFromCollection godoc
// @Summary 将文章移出合集
// @Description 移除当前用户拥有文章与指定合集之间的关联。
// @Tags blog
// @Produce json
// @Param id path string true "文章 UUID"
// @Param collection_id path string true "合集 UUID"
// @Success 200 {object} handlers.MessageResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/posts/{id}/collections/{collection_id} [delete]
func (h *Handler) removePostFromCollection(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	postID, err := parsePostID(c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	collectionID, err := uuid.Parse(c.Param("collection_id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "collection_id must be a valid UUID"))
		return
	}
	var post model.Post
	if err := h.service.db.First(&post, "id = ?", postID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	if post.UserID != user.ID {
		httpx.Error(c, apperr.Forbidden("blog.post_forbidden", "You don't have permission to modify this post"))
		return
	}
	if post.ChannelID == nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Post is not assigned to a channel"))
		return
	}
	var collection model.Collection
	if err := h.service.db.First(&collection, "id = ?", collectionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.collection_not_found", "Collection not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	if collection.ChannelID != *post.ChannelID {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Collection does not belong to post channel"))
		return
	}
	if err := h.service.db.Model(&post).Association("Collections").Delete(&collection); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func (h *Handler) reorderCollectionPosts(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	collectionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "collection_id must be a valid UUID"))
		return
	}

	var req reorderCollectionPostsInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	if len(req.PostIDs) == 0 {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "post_ids is required"))
		return
	}

	var collection model.Collection
	if err := h.service.db.Preload("Channel").First(&collection, "id = ?", collectionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.collection_not_found", "Collection not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	if collection.Channel == nil || collection.Channel.UserID == nil || *collection.Channel.UserID != user.ID {
		httpx.Error(c, apperr.Forbidden("blog.collection_forbidden", "You don't have permission to reorder this collection"))
		return
	}

	postIDs := make([]uuid.UUID, 0, len(req.PostIDs))
	seen := make(map[uuid.UUID]struct{}, len(req.PostIDs))
	for _, raw := range req.PostIDs {
		postID, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "post_ids must contain valid UUIDs"))
			return
		}
		if _, exists := seen[postID]; exists {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "post_ids must be unique"))
			return
		}
		seen[postID] = struct{}{}
		postIDs = append(postIDs, postID)
	}

	if err := h.service.reorderCollectionPosts(collection, postIDs, user.ID); err != nil {
		httpx.Error(c, err)
		return
	}

	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func (h *Handler) updatePostStatus(c *gin.Context, status string) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	postID, err := parsePostID(c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var post model.Post
	if err := h.service.db.First(&post, "id = ?", postID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	if post.UserID != user.ID {
		httpx.Error(c, apperr.Forbidden("blog.post_forbidden", "You don't have permission to modify this post"))
		return
	}
	if err := h.service.db.Model(&post).Update("status", status).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func (h *Handler) updatePostPin(c *gin.Context, pinned bool) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	postID, err := parsePostID(c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var post model.Post
	if err := h.service.db.First(&post, "id = ?", postID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	if post.UserID != user.ID {
		httpx.Error(c, apperr.Forbidden("blog.post_forbidden", "You don't have permission to modify this post"))
		return
	}
	if err := h.service.db.Model(&post).Update("pinned", pinned).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func parsePostID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", "postId must be a valid UUID")
	}
	return id, nil
}

func bindJSON(c *gin.Context, dst any) error {
	if err := c.ShouldBindJSON(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return apperr.BadRequest("validation.invalid_request", "request body must not be empty")
		}
		return apperr.BadRequest("validation.invalid_request", "request body must be valid JSON")
	}
	return nil
}

func normalizeBlogVisibility(raw string) string {
	switch strings.TrimSpace(raw) {
	case "followers", "private":
		return strings.TrimSpace(raw)
	default:
		return "public"
	}
}

func parseOptionalUUID(raw string) (*uuid.UUID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := uuid.Parse(trimmed)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func normalizeUUIDList(values []string) ([]string, error) {
	if len(values) == 0 {
		return []string{}, nil
	}
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		parsed, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return nil, err
		}
		stringID := parsed.String()
		if _, exists := seen[stringID]; exists {
			continue
		}
		seen[stringID] = struct{}{}
		normalized = append(normalized, stringID)
	}
	return normalized, nil
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

	collectionIDs := []string{}
	for _, collectionID := range strings.Split(draft.CollectionIDs, ",") {
		trimmed := strings.TrimSpace(collectionID)
		if trimmed == "" {
			continue
		}
		collectionIDs = append(collectionIDs, trimmed)
	}

	return blogDraftResponse{
		ID:            draft.ID,
		UserID:        draft.UserID,
		ContextKey:    draft.ContextKey,
		SourcePostID:  sourcePostID,
		Title:         draft.Title,
		Content:       draft.Content,
		Summary:       draft.Summary,
		CoverURL:      draft.CoverURL,
		Visibility:    draft.Visibility,
		AllowComments: draft.AllowComments,
		ChannelID:     channelID,
		CollectionIDs: collectionIDs,
		CreatedAt:     draft.CreatedAt,
		UpdatedAt:     draft.UpdatedAt,
	}
}

func ensureDefaultCollection(db *gorm.DB, channelID uuid.UUID) (*model.Collection, error) {
	var collection model.Collection
	err := db.Where("channel_id = ? AND is_default = ?", channelID, true).First(&collection).Error
	if err == nil {
		return &collection, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	collection = model.Collection{
		ChannelID:   channelID,
		Name:        ensureDefaultCollectionName(),
		Description: "默认合集",
		IsDefault:   true,
	}
	if err := db.Create(&collection).Error; err != nil {
		return nil, err
	}
	return &collection, nil
}
