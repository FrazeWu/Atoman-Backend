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

type channelInput struct {
	Name        string `json:"name" binding:"required"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
	ContentType string `json:"content_type"`
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
	CollectionID  *string  `json:"collection_id"`
	CollectionIDs []string `json:"collection_ids" swaggerignore:"true"`
}

type reorderCollectionPostsInput struct {
	PostIDs []string `json:"post_ids"`
}

type blogDraftInput struct {
	ContextKey    string `json:"context_key" binding:"required"`
	SourcePostID  string `json:"source_post_id"`
	Title         string `json:"title"`
	Content       string `json:"content"`
	Summary       string `json:"summary"`
	CoverURL      string `json:"cover_url"`
	Visibility    string `json:"visibility"`
	AllowComments *bool  `json:"allow_comments"`
	ChannelID     string `json:"channel_id"`
	CollectionID  string `json:"collection_id"`
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
	CollectionID  *string   `json:"collection_id,omitempty"`
	CreatedAt     any       `json:"created_at"`
	UpdatedAt     any       `json:"updated_at"`
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.GET("/seo/posts/:id", h.getSEOPost)
	group.GET("/seo/sitemap", h.listSEOSitemap)
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
	group.GET("/posts/:id/versions", h.listPostVersions)
	group.POST("/posts/:id/versions/:version/restore", h.restorePostVersion)
	group.GET("/posts/:id", h.getPost)
	group.POST("/posts", h.createPost)
	group.PUT("/posts/:id", h.updatePost)
	group.DELETE("/posts/:id", h.deletePost)
	group.POST("/posts/:id/publish", h.publishPost)
	group.POST("/posts/:id/unpublish", h.unpublishPost)
	group.POST("/posts/:id/pin", h.pinPost)
	group.POST("/posts/:id/unpin", h.unpinPost)
	group.PUT("/collections/:id/posts/order", h.reorderCollectionPosts)
	group.GET("/drafts", h.getBlogDraft)
	group.PUT("/drafts", h.putBlogDraft)
	group.DELETE("/drafts", h.deleteBlogDraft)
}

// getSEOPost godoc
// @Summary 获取公开文章 SEO 元数据
// @Description 仅返回已发布且公开的文章元数据，不计入阅读数。
// @Tags blog
// @Produce json
// @Param id path string true "文章 UUID"
// @Success 200 {object} SEOPostResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/blog/seo/posts/{id} [get]
func (h *Handler) getSEOPost(c *gin.Context) {
	postID, err := parsePostID(c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	post, err := h.service.GetSEOPost(postID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, post)
}

// listSEOSitemap godoc
// @Summary 获取公开文章站点地图数据
// @Description 返回全部已发布且公开的文章路径及最后修改时间。
// @Tags blog
// @Produce json
// @Success 200 {object} SEOSitemapResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/blog/seo/sitemap [get]
func (h *Handler) listSEOSitemap(c *gin.Context) {
	items, err := h.service.ListSEOSitemap()
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, items)
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
		Where("visibility = ? OR visibility = ?", "", "public").
		Preload("User").
		Order("COALESCE(published_at, created_at) DESC").
		Order("created_at DESC").
		Order("id DESC").
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
	channel, err := h.service.CreateChannel(user, req.Name, req.Slug, req.Description, req.CoverURL, req.ContentType)
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

// listBookmarks godoc
// @Summary 获取文章收藏
// @Tags blog
// @Produce json
// @Param folder_id query string false "收藏夹 UUID"
// @Param sort query string false "排序" Enums(latest,popular)
// @Success 200 {array} model.Bookmark
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmarks [get]
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

// createBookmark godoc
// @Summary 收藏文章到指定收藏夹
// @Tags blog
// @Accept json
// @Produce json
// @Param input body bookmarkInput true "收藏输入"
// @Success 201 {object} model.Bookmark
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmarks [post]
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

// deleteBookmark godoc
// @Summary 取消文章收藏
// @Tags blog
// @Param id path string true "收藏 UUID"
// @Success 200 {object} handlers.MessageResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmarks/{id} [delete]
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

// listBookmarkFolders godoc
// @Summary 获取收藏夹
// @Tags blog
// @Success 200 {array} model.BookmarkFolder
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmark-folders [get]
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

// createBookmarkFolder godoc
// @Summary 新建收藏夹
// @Tags blog
// @Accept json
// @Param input body bookmarkFolderInput true "收藏夹输入"
// @Success 201 {object} model.BookmarkFolder
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmark-folders [post]
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

// deleteBookmarkFolder godoc
// @Summary 删除收藏夹
// @Description 收藏会迁移到默认收藏夹。
// @Tags blog
// @Param id path string true "收藏夹 UUID"
// @Success 200 {object} handlers.MessageResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmark-folders/{id} [delete]
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
		publishedAt := p.CreatedAt
		if p.PublishedAt != nil {
			publishedAt = *p.PublishedAt
		}
		pubDate := publishedAt.Format(time.RFC1123Z)
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
      <link>%s/posts/post/%s</link>
      <guid isPermaLink="true">%s/posts/post/%s</guid>
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
	page, pageSize := httpx.PageParams(c)
	query := h.service.db.Model(&model.Post{}).Preload("User").Preload("Channel").Preload("Collection").Where("status = ?", "published")
	query = applyPostListVisibility(query, currentViewerID(c))

	if userID := c.Query("user_id"); userID != "" {
		query = query.Where("user_id = ?", userID)
	}
	channelID := c.Query("channel_id")
	if channelID != "" {
		query = query.Where("channel_id = ?", channelID)
	}
	if collectionID := c.Query("collection_id"); collectionID != "" {
		query = query.Where("posts.collection_id = ?", collectionID)
		query = query.Order("posts.collection_position ASC")
	} else if channelID != "" {
		query = query.Order("pinned DESC, published_at DESC, posts.id DESC")
	} else {
		query = query.Order("published_at DESC, posts.id DESC")
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		searchLike := "%" + q + "%"
		query = query.Where("(LOWER(title) LIKE LOWER(?) OR LOWER(summary) LIKE LOWER(?) OR LOWER(content) LIKE LOWER(?))", searchLike, searchLike, searchLike)
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Distinct("posts.id").Count(&total).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	if err := query.Offset(httpx.Offset(page, pageSize)).Limit(pageSize).Find(&posts).Error; err != nil {
		httpx.Error(c, err)
		return
	}

	items := make([]PostListItemDTO, 0, len(posts))
	for _, post := range posts {
		likes, err := h.service.CountPostLikes(post.ID)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		var comments int64
		if err := h.service.db.Model(&model.Comment{}).Where("target_type = ? AND target_id = ? AND status = ?", "post", post.ID, "visible").Count(&comments).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		var bookmarks int64
		if err := h.service.db.Model(&model.Bookmark{}).Where("post_id = ?", post.ID).Count(&bookmarks).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		items = append(items, PostListItemDTO{Post: post, LikesCount: likes, CommentsCount: comments, BookmarksCount: bookmarks})
	}

	httpx.List(c, items, page, pageSize, total)
}

func applyPostListVisibility(query *gorm.DB, viewerID *uuid.UUID) *gorm.DB {
	public := query.Where("(visibility = ? OR visibility = ?)", "", "public")
	if viewerID == nil {
		return public
	}

	subscribedChannelIDs := query.Session(&gorm.Session{NewDB: true}).
		Table("feed_sources").
		Select("feed_sources.source_id").
		Joins("JOIN subscriptions ON subscriptions.feed_source_id = feed_sources.id").
		Where("subscriptions.user_id = ?", *viewerID).
		Where("feed_sources.source_type = ?", "internal_channel").
		Where("feed_sources.deleted_at IS NULL AND subscriptions.deleted_at IS NULL")

	return query.Where(
		"(visibility = ? OR visibility = ? OR user_id = ? OR (visibility = ? AND channel_id IN (?)))",
		"", "public", *viewerID, "followers", subscribedChannelIDs,
	)
}

// listRecommendedPosts godoc
// @Summary 获取博客综合推荐
// @Tags blog
// @Produce json
// @Param mode query string false "推荐模式" Enums(hot,featured,discover)
// @Param page query int false "页码"
// @Param page_size query int false "每页数量"
// @Success 200 {array} RecommendationItemDTO
// @Router /api/v1/blog/recommend/posts [get]
func (h *Handler) listRecommendedPosts(c *gin.Context) {
	mode, err := parseRecommendationMode(c.DefaultQuery("mode", "hot"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	page, pageSize := httpx.PageParams(c)
	items, total, err := h.service.RecommendPostsByMode(mode, currentViewerID(c), page, pageSize)
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
	if err := h.service.db.Preload("User").Preload("Channel").Preload("Collection").First(&post, "id = ?", postID).Error; err != nil {
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
	if post.Status == "published" && (viewerID == nil || *viewerID != post.UserID) {
		if err := h.service.db.Model(&model.Post{}).Where("id = ?", post.ID).
			UpdateColumn("view_count", gorm.Expr("view_count + ?", 1)).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		post.ViewCount++
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
	var commentsCount int64
	if err := h.service.db.Model(&model.Comment{}).
		Where("target_type = ? AND target_id = ? AND status = ?", "post", post.ID, "visible").
		Count(&commentsCount).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	var bookmarksCount int64
	if err := h.service.db.Model(&model.Bookmark{}).Where("post_id = ?", post.ID).Count(&bookmarksCount).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	var channelFollowersCount int64
	if post.ChannelID != nil {
		if err := h.service.db.Model(&model.Subscription{}).
			Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
			Where("feed_sources.source_type = ? AND feed_sources.source_id = ?", "internal_channel", *post.ChannelID).
			Count(&channelFollowersCount).Error; err != nil {
			httpx.Error(c, err)
			return
		}
	}

	httpx.OK(c, http.StatusOK, struct {
		model.Post
		Liked                 bool  `json:"liked"`
		LikesCount            int64 `json:"likes_count"`
		CommentsCount         int64 `json:"comments_count"`
		BookmarksCount        int64 `json:"bookmarks_count"`
		ChannelFollowersCount int64 `json:"channel_followers_count"`
	}{
		Post:                  post,
		Liked:                 liked,
		LikesCount:            likesCount,
		CommentsCount:         commentsCount,
		BookmarksCount:        bookmarksCount,
		ChannelFollowersCount: channelFollowersCount,
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

// listPostVersions godoc
// @Summary 获取文章版本历史
// @Tags blog
// @Produce json
// @Param id path string true "文章 UUID"
// @Success 200 {array} model.BlogPostVersion
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/posts/{id}/versions [get]
func (h *Handler) listPostVersions(c *gin.Context) {
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
	versions, err := h.service.ListPostVersions(user, postID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, versions)
}

// restorePostVersion godoc
// @Summary 恢复文章版本
// @Tags blog
// @Produce json
// @Param id path string true "文章 UUID"
// @Param version path int true "版本号"
// @Success 200 {object} model.Post
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/posts/{id}/versions/{version}/restore [post]
func (h *Handler) restorePostVersion(c *gin.Context) {
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
	version, err := strconv.Atoi(c.Param("version"))
	if err != nil || version < 1 {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "version must be a positive integer"))
		return
	}
	post, err := h.service.RestorePostVersion(user, postID, version)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, post)
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

	if len(req.CollectionIDs) > 0 {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "collection_ids is no longer supported"))
		return
	}
	if req.CollectionID != nil {
		collectionID, err := uuid.Parse(strings.TrimSpace(*req.CollectionID))
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
		if req.ChannelID != nil {
			channelID, err := uuid.Parse(strings.TrimSpace(*req.ChannelID))
			if err != nil || channelID != collection.ChannelID {
				httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Collection does not belong to selected channel"))
				return
			}
		}
		updates["collection_id"] = collection.ID
		updates["channel_id"] = collection.ChannelID
		if post.CollectionID == nil || *post.CollectionID != collection.ID {
			var maxPosition int
			if err := h.service.db.Model(&model.Post{}).Where("collection_id = ?", collection.ID).Select("COALESCE(MAX(collection_position), -1)").Scan(&maxPosition).Error; err != nil {
				httpx.Error(c, err)
				return
			}
			updates["collection_position"] = maxPosition + 1
		}
	} else if post.CollectionID == nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "collection_id is required"))
		return
	}

	if req.Status == "published" || req.Status == "draft" {
		updates["status"] = req.Status
	}
	if req.AllowComments != nil {
		updates["allow_comments"] = *req.AllowComments
	}
	if req.Status == "published" && post.PublishedAt == nil {
		now := time.Now().UTC()
		updates["published_at"] = now
	}
	if err := h.service.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&post).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Preload("Channel").Preload("Collection").First(&post, "id = ?", post.ID).Error; err != nil {
			return err
		}
		if post.Status == "published" {
			return saveBlogPostVersion(tx, post, user.ID)
		}
		return nil
	}); err != nil {
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
		CollectionID:  collectionID,
	}
	if err := h.service.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "context_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"source_post_id", "title", "content", "summary", "cover_url", "visibility", "allow_comments", "channel_id", "collection_id", "updated_at"}),
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
	wasPublished := post.Status == "published"
	if err := h.service.db.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{"status": status}
		if status == "published" && post.PublishedAt == nil {
			updates["published_at"] = time.Now().UTC()
		}
		if err := tx.Model(&post).Updates(updates).Error; err != nil {
			return err
		}
		if status != "published" || wasPublished {
			return nil
		}
		if err := tx.Preload("Channel").Preload("Collection").First(&post, "id = ?", post.ID).Error; err != nil {
			return err
		}
		return saveBlogPostVersion(tx, post, user.ID)
	}); err != nil {
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
		CollectionID:  collectionID,
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
