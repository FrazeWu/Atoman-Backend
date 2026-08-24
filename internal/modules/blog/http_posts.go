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

	"atoman/internal/feedlanguage"
	"atoman/internal/model"
	"atoman/internal/modules/lifecycle"
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
	userID, err := parseOptionalUUID(c.Query("user_id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "user_id must be a valid uuid"))
		return
	}
	channelID, err := parseOptionalUUID(c.Query("channel_id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "channel_id must be a valid uuid"))
		return
	}
	collectionID, err := parseOptionalUUID(c.Query("collection_id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "collection_id must be a valid uuid"))
		return
	}
	var canonicalCollectionID *uuid.UUID
	if collectionID != nil {
		resolved, err := canonicalBlogCollectionID(h.service.db, *collectionID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				httpx.List(c, []BlogContentListItemDTO{}, page, pageSize, 0)
				return
			}
			httpx.Error(c, err)
			return
		}
		canonicalCollectionID = &resolved
	}
	query := canonicalBlogPostsQuery(h.service.db).Where("posts.status = ?", "published")
	query = ApplyPublishedPostListVisibility(query, currentViewerID(c))

	if userID != nil {
		query = query.Where("posts.author_id = ?", *userID)
	}
	if channelID != nil {
		query = query.Where("posts.channel_id = ?", *channelID)
	}
	if canonicalCollectionID != nil {
		query = query.Where("memberships.collection_id = ?", *canonicalCollectionID)
		query = query.Order("memberships.position ASC")
	} else if channelID != nil {
		query = query.Order("blog_extensions.pinned DESC, posts.published_at DESC, posts.id DESC")
	} else {
		query = query.Order("posts.published_at DESC, posts.id DESC")
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		searchLike := "%" + q + "%"
		query = query.Where("(LOWER(posts.title) LIKE LOWER(?) OR LOWER(posts.summary) LIKE LOWER(?) OR LOWER(blog_extensions.content) LIKE LOWER(?))", searchLike, searchLike, searchLike)
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Distinct("posts.id").Count(&total).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	var canonicalRows []canonicalBlogPostRow
	if err := query.Offset(httpx.Offset(page, pageSize)).Limit(pageSize).Find(&canonicalRows).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	posts, err = hydrateCanonicalBlogPosts(h.service.db, canonicalRows)
	if err != nil {
		httpx.Error(c, err)
		return
	}

	postDTOs, err := h.service.postDTOs(h.service.db, posts, currentViewerID(c))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	type postEngagementCount struct {
		PostID         uuid.UUID `gorm:"column:post_id"`
		LikesCount     int64     `gorm:"column:likes_count"`
		CommentsCount  int64     `gorm:"column:comments_count"`
		BookmarksCount int64     `gorm:"column:bookmarks_count"`
	}
	postIDs := make([]uuid.UUID, 0, len(posts))
	for _, post := range posts {
		postIDs = append(postIDs, post.ID)
	}
	countsByPostID := make(map[uuid.UUID]postEngagementCount, len(postIDs))
	if len(postIDs) > 0 {
		var counts []postEngagementCount
		if err := h.service.db.Table("content_entries AS posts").Select(`posts.id AS post_id,
			(SELECT COUNT(*) FROM likes WHERE likes.target_type = 'post' AND likes.target_id = posts.id AND likes.deleted_at IS NULL) AS likes_count,
			COALESCE((SELECT targets.comment_count FROM discussion_targets AS targets WHERE targets.kind = 'blog_post' AND targets.resource_id = posts.id AND targets.deleted_at IS NULL LIMIT 1), 0) AS comments_count,
			(SELECT COUNT(*) FROM bookmarks WHERE bookmarks.post_id = posts.id AND bookmarks.deleted_at IS NULL) AS bookmarks_count`).
			Where("posts.id IN ?", postIDs).Scan(&counts).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		for _, count := range counts {
			countsByPostID[count.PostID] = count
		}
	}
	items := make([]BlogContentListItemDTO, 0, len(posts))
	for index, post := range posts {
		count := countsByPostID[post.ID]
		items = append(items, BlogContentListItemDTO{
			BlogContentDTO: postDTOs[index], LikesCount: count.LikesCount,
			CommentsCount: count.CommentsCount, BookmarksCount: count.BookmarksCount,
		})
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
		"(posts.visibility = ? OR posts.visibility = ? OR posts.author_id = ? OR (posts.visibility = ? AND posts.channel_id IN (?)))",
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
// @Description 返回指定文章；未发布文章（草稿或定时发布）仅作者本人可查看。
// @Tags blog
// @Produce json
// @Param id path string true "文章 UUID"
// @Success 200 {object} BlogContentDTO
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
	if post, err = loadCanonicalBlogPost(h.service.db, postID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
			return
		}
		httpx.Error(c, err)
		return
	}

	viewerID := currentViewerID(c)
	if post.Status != "published" {
		if viewerID == nil || post.UserID != *viewerID {
			httpx.Error(c, apperr.Forbidden("blog.post_forbidden", "You don't have permission to view this unpublished post"))
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
		BlogContentDTO
		Liked                 bool  `json:"liked"`
		LikesCount            int64 `json:"likes_count"`
		CommentsCount         int64 `json:"comments_count"`
		BookmarksCount        int64 `json:"bookmarks_count"`
		ChannelFollowersCount int64 `json:"channel_followers_count"`
	}{
		BlogContentDTO:        postDTO,
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
// @Success 201 {object} BlogContentDTO
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
// @Success 200 {array} BlogContentVersionDTO
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
// @Success 200 {object} BlogContentDTO
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
	post, err := loadCanonicalBlogPost(h.service.db, postID)
	if err != nil {
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
	effectiveStatus := post.Status
	if req.Status == "published" || req.Status == "draft" {
		effectiveStatus = req.Status
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
		if effectiveChannelID != uuid.Nil && channelID != effectiveChannelID {
			httpx.Error(c, apperr.BadRequest("studio.cross_channel_move_not_supported", "Content cannot be moved between channels"))
			return
		}
		effectiveChannelID = channelID
	}
	requestedCollectionID := post.CollectionID
	collectionChanged := false
	if req.CollectionID != nil {
		collectionID, err := uuid.Parse(strings.TrimSpace(*req.CollectionID))
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid collection UUID"))
			return
		}
		requestedCollectionID = &collectionID
		collectionChanged = post.CollectionID == nil || *post.CollectionID != collectionID
	}
	shouldResolveCollection := req.CollectionID != nil || (!wasPublished && effectiveStatus == "published")
	if shouldResolveCollection && post.CollectionConflict && req.CollectionID == nil {
		httpx.Error(c, apperr.Conflict("studio.collection_conflict", "Choose one collection before publishing"))
		return
	}
	var collection *model.ContentCollection
	if shouldResolveCollection {
		collection, err = resolveBlogCollection(h.service.db, user.ID, effectiveChannelID, requestedCollectionID, effectiveStatus == "published")
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				httpx.Error(c, apperr.BadRequest("studio.invalid_collection_scope", "Collection must belong to the selected channel"))
				return
			}
			httpx.Error(c, err)
			return
		}
	}
	if effectiveStatus == "published" {
		if err := h.ensurePublishChannelAllowed(&effectiveChannelID); err != nil {
			httpx.Error(c, err)
			return
		}
	}

	entryUpdates := map[string]any{
		"title": strings.TrimSpace(req.Title), "summary": strings.TrimSpace(req.Summary),
		"cover_url": strings.TrimSpace(req.CoverURL), "visibility": normalizeBlogVisibility(req.Visibility),
		"status": effectiveStatus, "scheduled_at": nil,
	}
	if req.Status != "published" && req.Status != "draft" {
		delete(entryUpdates, "status")
		delete(entryUpdates, "scheduled_at")
	}
	if effectiveStatus == "published" && post.PublishedAt == nil {
		entryUpdates["published_at"] = time.Now().UTC()
	}
	if req.Status == "draft" {
		entryUpdates["published_at"] = post.PublishedAt
	}
	languageCode := feedlanguage.Detect(strings.Join([]string{req.Title, req.Summary, req.Content}, " "))
	extensionUpdates := map[string]any{"content": req.Content}
	if languageCode != "" {
		extensionUpdates["language_code"] = languageCode
	}

	if err := h.service.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.ContentEntry{}).Where("id = ?", postID).Updates(entryUpdates).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ContentBlogExtension{}).Where("content_id = ?", postID).Updates(extensionUpdates).Error; err != nil {
			return err
		}
		if shouldResolveCollection {
			if err := tx.Where("content_id = ?", postID).Delete(&model.ContentCollectionMembership{}).Error; err != nil {
				return err
			}
			if collection != nil {
				var maxPosition int
				if collectionChanged || post.CollectionID == nil {
					if err := tx.Model(&model.ContentCollectionMembership{}).Where("collection_id = ?", collection.ID).
						Select("COALESCE(MAX(position), -1)").Scan(&maxPosition).Error; err != nil {
						return err
					}
				} else {
					maxPosition = post.CollectionPosition - 1
				}
				if err := tx.Create(&model.ContentCollectionMembership{ContentID: postID, CollectionID: collection.ID, Position: maxPosition + 1}).Error; err != nil {
					return err
				}
			}
		}
		updated, err := loadCanonicalBlogPost(tx, postID)
		if err != nil {
			return err
		}
		if _, err := h.service.syncPostReferences(tx, updated); err != nil {
			return err
		}
		if updated.Status == "published" {
			if err := saveBlogPostVersion(tx, updated, user.ID); err != nil {
				return err
			}
			if !wasPublished {
				return lifecycle.NewService(tx).EnqueuePublication("blog", postID)
			}
		}
		return nil
	}); err != nil {
		httpx.Error(c, err)
		return
	}
	post, err = loadCanonicalBlogPost(h.service.db, postID)
	if err != nil {
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

	post, err := loadCanonicalBlogPost(h.service.db, postID)
	if err != nil {
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
		if err := tx.Where("content_id = ?", post.ID).Delete(&model.ContentCollectionMembership{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&model.ContentBlogExtension{}, "content_id = ?", post.ID).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ContentEntry{}, "id = ?", post.ID).Error
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

	collection, err := h.service.repo.GetCollection(collectionID)
	if err != nil {
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
	post, err := loadCanonicalBlogPost(h.service.db, postID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	if post.UserID != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		httpx.Error(c, apperr.Forbidden("blog.post_forbidden", "You don't have permission to modify this post"))
		return
	}
	if status == "published" && post.ChannelID != nil {
		if err := h.ensurePublishChannelAllowed(post.ChannelID); err != nil {
			httpx.Error(c, err)
			return
		}
	}
	wasPublished := post.Status == "published"
	wasPublic := isPublicPostState(post.Status, post.Visibility)
	if err := h.service.db.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{"status": status, "scheduled_at": nil}
		if status == "published" && post.PublishedAt == nil {
			updates["published_at"] = time.Now().UTC()
		}
		if err := tx.Model(&model.ContentEntry{}).Where("id = ?", postID).Updates(updates).Error; err != nil {
			return err
		}
		updated, err := loadCanonicalBlogPost(tx, postID)
		if err != nil {
			return err
		}
		if _, err := h.service.syncPostReferences(tx, updated); err != nil {
			return err
		}
		if status == "published" && !wasPublished {
			if err := saveBlogPostVersion(tx, updated, user.ID); err != nil {
				return err
			}
			return lifecycle.NewService(tx).EnqueuePublication("blog", postID)
		}
		return nil
	}); err != nil {
		httpx.Error(c, err)
		return
	}
	post, err = loadCanonicalBlogPost(h.service.db, postID)
	if err != nil {
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
	post, err := loadCanonicalBlogPost(h.service.db, postID)
	if err != nil {
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
	if err := h.service.db.Model(&model.ContentBlogExtension{}).Where("content_id = ?", postID).Update("pinned", pinned).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func (h *Handler) ensurePublishChannelAllowed(channelID *uuid.UUID) error {
	if channelID == nil || *channelID == uuid.Nil {
		return nil
	}
	channel, err := h.service.repo.GetChannel(*channelID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("blog.channel_not_found", "Channel not found")
	}
	if err != nil {
		return err
	}
	if isChannelBanned(channel) {
		return apperr.Forbidden("blog.channel_banned", "Banned channel cannot publish posts")
	}
	return nil
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
