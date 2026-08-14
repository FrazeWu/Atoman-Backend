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
	"atoman/internal/modules/lifecycle"
	studioapi "atoman/internal/modules/studio"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"atoman/internal/platform/indexnow"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

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
	query = ApplyPublishedPostListVisibility(query, currentViewerID(c))

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

	postDTOs, err := h.service.postDTOs(h.service.db, posts, currentViewerID(c))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	items := make([]PostListItemDTO, 0, len(posts))
	for index, post := range posts {
		likes, err := h.service.CountPostLikes(post.ID)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		var comments int64
		if err := h.service.db.Model(&model.DiscussionTarget{}).
			Select("COALESCE(MAX(comment_count), 0)").
			Where("kind = ? AND resource_id = ?", "blog_post", post.ID).
			Scan(&comments).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		var bookmarks int64
		if err := h.service.db.Model(&model.Bookmark{}).Where("post_id = ?", post.ID).Count(&bookmarks).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		items = append(items, PostListItemDTO{PostDTO: postDTOs[index], LikesCount: likes, CommentsCount: comments, BookmarksCount: bookmarks})
	}

	httpx.List(c, items, page, pageSize, total)
}

func ApplyPublishedPostListVisibility(query *gorm.DB, viewerID *uuid.UUID) *gorm.DB {
	if viewerID == nil {
		return query.Where("(posts.visibility = ? OR posts.visibility = ?)", "", "public")
	}

	subscribedChannelIDs := query.Session(&gorm.Session{NewDB: true}).
		Table("feed_sources").
		Select("feed_sources.source_id").
		Joins("JOIN subscriptions ON subscriptions.feed_source_id = feed_sources.id").
		Where("subscriptions.user_id = ?", *viewerID).
		Where("feed_sources.source_type = ?", "internal_channel").
		Where("feed_sources.deleted_at IS NULL AND subscriptions.deleted_at IS NULL")

	return query.Where(
		"(posts.visibility = ? OR posts.visibility = ? OR posts.user_id = ? OR (posts.visibility = ? AND posts.channel_id IN (?)))",
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
		allowed, err := CanViewPublishedPost(h.service.db, viewerID, post)
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
		if post.ChannelID != nil {
			if err := studioapi.NewService(h.service.db).RecordMetricEvent(*post.ChannelID, studioapi.ModuleBlog, post.ID, "view"); err != nil {
				httpx.Error(c, err)
				return
			}
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
	var commentsCount int64
	if err := h.service.db.Model(&model.DiscussionTarget{}).
		Select("COALESCE(MAX(comment_count), 0)").
		Where("kind = ? AND resource_id = ?", "blog_post", post.ID).
		Scan(&commentsCount).Error; err != nil {
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

	postDTO, err := h.service.postDTO(h.service.db, post, viewerID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, struct {
		PostDTO
		Liked                 bool  `json:"liked"`
		LikesCount            int64 `json:"likes_count"`
		CommentsCount         int64 `json:"comments_count"`
		BookmarksCount        int64 `json:"bookmarks_count"`
		ChannelFollowersCount int64 `json:"channel_followers_count"`
	}{
		PostDTO:               postDTO,
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

func CanViewPublishedPost(db *gorm.DB, viewerID *uuid.UUID, post model.Post) (bool, error) {
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
	postDTO, err := h.service.postDTO(h.service.db, post, &user.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, postDTO)
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
	postDTO, err := h.service.postDTO(h.service.db, post, &user.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, postDTO)
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
	if visibility := strings.TrimSpace(req.Visibility); visibility != "" {
		if _, ok := allowedPostVisibilities[visibility]; !ok {
			httpx.Error(c, apperr.BadRequest("blog.invalid_visibility", "visibility is invalid"))
			return
		}
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
	wasPublished := post.Status == "published"
	wasPublic := isPublicPostState(post.Status, post.Visibility)

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
	effectiveStatus := post.Status
	if req.Status == "published" || req.Status == "draft" {
		effectiveStatus = req.Status
		updates["status"] = req.Status
	}
	effectiveChannelID := uuid.Nil
	if post.ChannelID != nil {
		effectiveChannelID = *post.ChannelID
	}
	if req.ChannelID != nil {
		channelID, err := uuid.Parse(strings.TrimSpace(*req.ChannelID))
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid channel UUID"))
			return
		}
		effectiveChannelID = channelID
		updates["channel_id"] = channelID
	}
	effectiveCollectionIDs := make([]uuid.UUID, 0, 1)
	if post.CollectionID != nil {
		effectiveCollectionIDs = append(effectiveCollectionIDs, *post.CollectionID)
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
		effectiveCollectionIDs = []uuid.UUID{collection.ID}
		if effectiveChannelID == uuid.Nil {
			effectiveChannelID = collection.ChannelID
		}
		updates["collection_id"] = collection.ID
		updates["channel_id"] = effectiveChannelID
		if post.CollectionID == nil || *post.CollectionID != collection.ID {
			var maxPosition int
			if err := h.service.db.Model(&model.Post{}).Where("collection_id = ?", collection.ID).Select("COALESCE(MAX(collection_position), -1)").Scan(&maxPosition).Error; err != nil {
				httpx.Error(c, err)
				return
			}
			updates["collection_position"] = maxPosition + 1
		}
	}
	if err := studioapi.NewService(h.service.db).ValidateContentScope(user.ID, effectiveChannelID, studioapi.ModuleBlog, effectiveCollectionIDs, effectiveStatus == "published"); err != nil {
		httpx.Error(c, err)
		return
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
		if _, err := h.service.syncPostReferences(tx, post); err != nil {
			return err
		}
		if post.Status == "published" {
			if err := saveBlogPostVersion(tx, post, user.ID); err != nil {
				return err
			}
			if !wasPublished {
				return lifecycle.NewService(tx).EnqueuePublication("blog", post.ID)
			}
		}
		return nil
	}); err != nil {
		httpx.Error(c, err)
		return
	}
	postDTO, err := h.service.postDTO(h.service.db, post, &user.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if wasPublic || isPublicPostState(post.Status, post.Visibility) {
		indexnow.NotifyPaths(seoPostPath(post.ID))
	}
	httpx.OK(c, http.StatusOK, postDTO)
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
	if err := h.service.db.Transaction(func(tx *gorm.DB) error {
		if err := h.service.references.RemoveSource(tx, "post", post.ID); err != nil {
			return err
		}
		return tx.Delete(&post).Error
	}); err != nil {
		httpx.Error(c, err)
		return
	}
	if isPublicPostState(post.Status, post.Visibility) {
		indexnow.NotifyPaths(seoPostPath(post.ID))
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func (h *Handler) publishPost(c *gin.Context) { h.updatePostStatus(c, "published") }

func (h *Handler) unpublishPost(c *gin.Context) { h.updatePostStatus(c, "draft") }

func (h *Handler) pinPost(c *gin.Context) { h.updatePostPin(c, true) }

func (h *Handler) unpinPost(c *gin.Context) { h.updatePostPin(c, false) }

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
	wasPublic := isPublicPostState(post.Status, post.Visibility)
	if err := h.service.db.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{"status": status}
		if status == "published" && post.PublishedAt == nil {
			updates["published_at"] = time.Now().UTC()
		}
		if err := tx.Model(&post).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.First(&post, "id = ?", post.ID).Error; err != nil {
			return err
		}
		if _, err := h.service.syncPostReferences(tx, post); err != nil {
			return err
		}
		if status != "published" || wasPublished {
			return nil
		}
		if err := tx.Preload("Channel").Preload("Collection").First(&post, "id = ?", post.ID).Error; err != nil {
			return err
		}
		if err := saveBlogPostVersion(tx, post, user.ID); err != nil {
			return err
		}
		return lifecycle.NewService(tx).EnqueuePublication("blog", post.ID)
	}); err != nil {
		httpx.Error(c, err)
		return
	}
	if wasPublic || isPublicPostState(post.Status, post.Visibility) {
		indexnow.NotifyPaths(seoPostPath(post.ID))
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

func isPublicPostState(status, visibility string) bool {
	return status == "published" && (visibility == "" || visibility == "public")
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
