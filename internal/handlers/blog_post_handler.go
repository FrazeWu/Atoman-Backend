package handlers

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"atoman/internal/middleware"
	"atoman/internal/model"
)

// SetupBlogPostRoutes configures blog post routes
func SetupBlogPostRoutes(router *gin.Engine, db *gorm.DB) {
	blog := router.Group("/api/blog")
	{
		blog.GET("/posts", GetPosts(db))
		blog.GET("/posts/:id", middleware.OptionalAuthMiddleware(), GetPost(db))

		// Protected routes
		protected := blog.Group("")
		protected.Use(middleware.AuthMiddleware())
		{
			protected.POST("/posts", CreatePost(db))
			protected.PUT("/posts/:id", UpdatePost(db))
			protected.DELETE("/posts/:id", DeletePost(db))

			protected.GET("/drafts", GetBlogDraft(db))
			protected.PUT("/drafts", PutBlogDraft(db))
			protected.DELETE("/drafts", DeleteBlogDraft(db))

			protected.POST("/posts/:id/publish", PublishPost(db))
			protected.POST("/posts/:id/unpublish", UnpublishPost(db))
			protected.POST("/posts/:id/pin", PinPost(db))
			protected.POST("/posts/:id/unpin", UnpinPost(db))

			protected.GET("/posts/drafts", GetDrafts(db))

			protected.POST("/posts/:id/collections", AddPostToCollection(db))
			protected.DELETE("/posts/:id/collections/:collection_id", RemovePostFromCollection(db))
		}
	}
}

// PostInput represents the request body for creating/updating a post
type PostInput struct {
	Title         string   `json:"title" binding:"required"`
	Content       string   `json:"content" binding:"required"`
	Summary       string   `json:"summary"`
	CoverURL      string   `json:"cover_url"`
	Visibility    string   `json:"visibility"`
	AllowComments *bool    `json:"allow_comments"`
	Status        string   `json:"status"` // "draft" or "published"
	ChannelID     *string  `json:"channel_id"`
	CollectionIDs []string `json:"collection_ids"`
}

// CollectionActionInput represents the request body for adding a post to a collection
type CollectionActionInput struct {
	CollectionID uuid.UUID `json:"collection_id" binding:"required"`
}

type BlogDraftInput struct {
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

type BlogDraftResponse struct {
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

// GetPosts returns a list of published posts, optionally filtered
// GetPosts godoc
// @Summary 获取已发布文章列表
// @Description 返回已发布文章，可按用户、频道或合集筛选。
// @Tags blog-posts
// @Produce json
// @Param user_id query string false "用户 UUID"
// @Param channel_id query string false "频道 UUID"
// @Param collection_id query string false "合集 UUID"
// @Param limit query int false "返回数量上限"
// @Success 200 {object} PostListResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/blog/posts [get]
func GetPosts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var posts []model.Post
		limit := boundedListLimit(c.Query("limit"), 0, 40)
		query := db.Preload("User").Preload("Channel").Preload("Collections").Where("status = ?", "published")

		if userID := c.Query("user_id"); userID != "" {
			query = query.Where("user_id = ?", userID)
		}

		if channelID := c.Query("channel_id"); channelID != "" {
			query = query.Where("channel_id = ?", channelID)
		}

		if collectionID := c.Query("collection_id"); collectionID != "" {
			query = query.Joins("JOIN post_collections pc ON pc.post_id = posts.id").
				Where("pc.collection_id = ?", collectionID)
		}

		query = query.Order("pinned DESC, created_at DESC")
		if limit > 0 {
			query = query.Limit(limit)
		}

		if err := query.Find(&posts).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch posts"})
			return
		}

		viewerID := currentBlogViewerID(c)
		visiblePosts := make([]model.Post, 0, len(posts))
		for _, post := range posts {
			allowed, err := canViewPublishedBlogPost(db, viewerID, post)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to evaluate post visibility"})
				return
			}
			if allowed {
				visiblePosts = append(visiblePosts, post)
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": visiblePosts, "message": "ok"})
	}
}

// GetPost returns a specific post by ID
// GetPost godoc
// @Summary 获取文章详情
// @Description 返回指定文章；若文章为草稿，则仅作者本人可查看。
// @Tags blog-posts
// @Produce json
// @Param id path string true "文章 UUID"
// @Success 200 {object} PostResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/blog/posts/{id} [get]
func GetPost(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var post model.Post

		if err := db.Preload("User").Preload("Channel").Preload("Collections").First(&post, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Post not found"})
			return
		}

		viewerID := currentBlogViewerID(c)
		if post.Status == "draft" {
			if viewerID == nil || post.UserID != *viewerID {
				c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to view this draft"})
				return
			}
		} else {
			allowed, err := canViewPublishedBlogPost(db, viewerID, post)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to evaluate post visibility"})
				return
			}
			if !allowed {
				c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to view this post"})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": post, "message": "ok"})
	}
}

// CreatePost creates a new post for the authenticated user
// CreatePost godoc
// @Summary 创建文章
// @Description 为当前用户创建文章，可选择草稿或已发布状态。
// @Tags blog-posts
// @Accept json
// @Produce json
// @Param input body PostInput true "文章输入"
// @Success 201 {object} PostResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/blog/posts [post]
func CreatePost(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input PostInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var channelID *uuid.UUID
		var selectedCollections []model.Collection
		if input.ChannelID != nil && strings.TrimSpace(*input.ChannelID) != "" {
			parsedChannelID, err := uuid.Parse(strings.TrimSpace(*input.ChannelID))
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel UUID"})
				return
			}

			var channel model.Channel
			if err := db.First(&channel, "id = ?", parsedChannelID).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "Channel not found"})
				return
			}

			if !ownsChannel(channel.UserID, userID) {
				c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to create post in this channel"})
				return
			}

			channelID = &parsedChannelID

			for _, collectionIDStr := range input.CollectionIDs {
				collectionID, err := uuid.Parse(collectionIDStr)
				if err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid collection UUID"})
					return
				}

				var collection model.Collection
				if err := db.Preload("Channel").First(&collection, "id = ?", collectionID).Error; err != nil {
					c.JSON(http.StatusNotFound, gin.H{"error": "Collection not found"})
					return
				}

				if !ownsChannel(collection.Channel.UserID, userID) {
					c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to assign this collection"})
					return
				}

				if collection.ChannelID != parsedChannelID {
					c.JSON(http.StatusBadRequest, gin.H{"error": "Collection does not belong to selected channel"})
					return
				}

				selectedCollections = append(selectedCollections, collection)
			}
		}

		allowComments := true
		if input.AllowComments != nil {
			allowComments = *input.AllowComments
		}

		status := "draft"
		if input.Status == "published" {
			status = "published"
		}
		visibility := normalizeBlogVisibility(input.Visibility)

		post := model.Post{
			UserID:        userID,
			ChannelID:     channelID,
			Title:         input.Title,
			Content:       input.Content,
			Summary:       input.Summary,
			CoverURL:      input.CoverURL,
			Status:        status,
			Visibility:    visibility,
			AllowComments: allowComments,
		}

		if err := db.Create(&post).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create post"})
			return
		}

		if channelID != nil {
			defaultCollection, err := ensureDefaultCollection(db, *channelID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to ensure default collection"})
				return
			}

			if err := db.Model(&post).Association("Collections").Append(defaultCollection); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to attach default collection"})
				return
			}

			for _, collection := range selectedCollections {
				if collection.ID == defaultCollection.ID {
					continue
				}

				if err := db.Model(&post).Association("Collections").Append(&collection); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to assign collection"})
					return
				}
			}
		}

		if err := db.Preload("Channel").Preload("Collections").First(&post, "id = ?", post.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch created post"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": post, "message": "ok"})
	}
}

// UpdatePost updates an existing post (only by owner)
// UpdatePost godoc
// @Summary 更新文章
// @Description 更新当前用户拥有的文章内容、归属频道和合集。
// @Tags blog-posts
// @Accept json
// @Produce json
// @Param id path string true "文章 UUID"
// @Param input body PostInput true "文章输入"
// @Success 200 {object} PostResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/blog/posts/{id} [put]
func UpdatePost(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var input PostInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var post model.Post
		if err := db.First(&post, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Post not found"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if post.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to update this post"})
			return
		}

		updates := map[string]interface{}{
			"title":      input.Title,
			"content":    input.Content,
			"summary":    input.Summary,
			"cover_url":  input.CoverURL,
			"visibility": normalizeBlogVisibility(input.Visibility),
		}

		targetChannelID := post.ChannelID
		if input.ChannelID != nil {
			trimmedChannelID := strings.TrimSpace(*input.ChannelID)
			if trimmedChannelID == "" {
				targetChannelID = nil
				updates["channel_id"] = nil
			} else {
				parsedChannelID, err := uuid.Parse(trimmedChannelID)
				if err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel UUID"})
					return
				}

				var channel model.Channel
				if err := db.First(&channel, "id = ?", parsedChannelID).Error; err != nil {
					c.JSON(http.StatusNotFound, gin.H{"error": "Channel not found"})
					return
				}

				if !ownsChannel(channel.UserID, userID) {
					c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to move post to this channel"})
					return
				}

				targetChannelID = &parsedChannelID
				updates["channel_id"] = parsedChannelID
			}
		}

		selectedCollections := make([]model.Collection, 0, len(input.CollectionIDs))
		if input.CollectionIDs != nil {
			for _, collectionIDStr := range input.CollectionIDs {
				collectionID, err := uuid.Parse(collectionIDStr)
				if err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid collection UUID"})
					return
				}

				var collection model.Collection
				if err := db.Preload("Channel").First(&collection, "id = ?", collectionID).Error; err != nil {
					c.JSON(http.StatusNotFound, gin.H{"error": "Collection not found"})
					return
				}

				if !ownsChannel(collection.Channel.UserID, userID) {
					c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to assign this collection"})
					return
				}

				if targetChannelID == nil || collection.ChannelID != *targetChannelID {
					c.JSON(http.StatusBadRequest, gin.H{"error": "Collection does not belong to selected channel"})
					return
				}

				selectedCollections = append(selectedCollections, collection)
			}
		}

		if input.Status == "published" || input.Status == "draft" {
			updates["status"] = input.Status
		}

		if input.AllowComments != nil {
			updates["allow_comments"] = *input.AllowComments
		}

		if err := db.Model(&post).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update post"})
			return
		}

		shouldUpdateCollections := input.ChannelID != nil || input.CollectionIDs != nil
		if shouldUpdateCollections {
			if targetChannelID != nil {
				defaultCollection, err := ensureDefaultCollection(db, *targetChannelID)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to ensure default collection"})
					return
				}

				collectionsToAssign := make([]model.Collection, 0, len(selectedCollections)+1)
				collectionsToAssign = append(collectionsToAssign, *defaultCollection)
				for _, collection := range selectedCollections {
					if collection.ID == defaultCollection.ID {
						continue
					}
					collectionsToAssign = append(collectionsToAssign, collection)
				}
				if err := db.Model(&post).Association("Collections").Replace(collectionsToAssign); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update post collections"})
					return
				}
			} else if err := db.Model(&post).Association("Collections").Clear(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear post collections"})
				return
			}
		}

		if err := db.Preload("Channel").Preload("Collections").First(&post, "id = ?", post.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch updated post"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": post, "message": "ok"})
	}
}

// DeletePost deletes a post (only by owner)
// DeletePost godoc
// @Summary 删除文章
// @Description 删除当前用户拥有的文章。
// @Tags blog-posts
// @Produce json
// @Param id path string true "文章 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/blog/posts/{id} [delete]
func DeletePost(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var post model.Post

		if err := db.First(&post, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Post not found"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if post.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to delete this post"})
			return
		}

		if err := db.Delete(&post).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete post"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// PublishPost changes post status to published
// PublishPost godoc
// @Summary 发布文章
// @Description 将当前用户拥有的文章状态切换为 published。
// @Tags blog-posts
// @Produce json
// @Param id path string true "文章 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/blog/posts/{id}/publish [post]
func PublishPost(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		updatePostStatus(c, db, "published")
	}
}

// UnpublishPost changes post status to draft
// UnpublishPost godoc
// @Summary 取消发布文章
// @Description 将当前用户拥有的文章状态切换为 draft。
// @Tags blog-posts
// @Produce json
// @Param id path string true "文章 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/blog/posts/{id}/unpublish [post]
func UnpublishPost(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		updatePostStatus(c, db, "draft")
	}
}

// PinPost sets post as pinned
// PinPost godoc
// @Summary 置顶文章
// @Description 将当前用户拥有的文章标记为置顶。
// @Tags blog-posts
// @Produce json
// @Param id path string true "文章 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/blog/posts/{id}/pin [post]
func PinPost(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		updatePostPin(c, db, true)
	}
}

// UnpinPost removes pinned status
// UnpinPost godoc
// @Summary 取消置顶文章
// @Description 取消当前用户拥有文章的置顶状态。
// @Tags blog-posts
// @Produce json
// @Param id path string true "文章 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/blog/posts/{id}/unpin [post]
func UnpinPost(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		updatePostPin(c, db, false)
	}
}

// GetDrafts returns a list of drafts for the authenticated user
// GetDrafts godoc
// @Summary 获取我的草稿文章
// @Description 返回当前登录用户的文章草稿列表。
// @Tags blog-posts
// @Produce json
// @Success 200 {object} PostListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/blog/posts/drafts [get]
func GetDrafts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var posts []model.Post
		if err := db.Preload("Collections").Where("user_id = ? AND status = ?", userID, "draft").Order("updated_at DESC").Find(&posts).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch drafts"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": posts, "message": "ok"})
	}
}

// GetBlogDraft godoc
// @Summary 获取编辑器草稿
// @Description 按 context_key 获取当前用户保存的编辑器草稿。
// @Tags blog-posts
// @Produce json
// @Param context_key query string true "编辑上下文 key"
// @Success 200 {object} BlogDraftEnvelope
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/blog/drafts [get]
func GetBlogDraft(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		contextKey := strings.TrimSpace(c.Query("context_key"))
		if contextKey == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "context_key required"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var draft model.BlogDraft
		if err := db.Where("user_id = ? AND context_key = ?", userID, contextKey).First(&draft).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Draft not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": buildBlogDraftResponse(draft), "message": "ok"})
	}
}

// PutBlogDraft godoc
// @Summary 保存编辑器草稿
// @Description 创建或更新当前用户在指定 context_key 下的编辑器草稿。
// @Tags blog-posts
// @Accept json
// @Produce json
// @Param input body BlogDraftInput true "草稿输入"
// @Success 200 {object} BlogDraftEnvelope
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/blog/drafts [put]
func PutBlogDraft(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input BlogDraftInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		sourcePostID, err := parseOptionalUUID(input.SourcePostID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid source_post_id"})
			return
		}

		channelID, err := parseOptionalUUID(input.ChannelID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel_id"})
			return
		}

		collectionIDs, err := normalizeUUIDList(input.CollectionIDs)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid collection_ids"})
			return
		}

		allowComments := true
		if input.AllowComments != nil {
			allowComments = *input.AllowComments
		}

		visibility := normalizeBlogVisibility(input.Visibility)

		draft := model.BlogDraft{
			UserID:        userID,
			ContextKey:    strings.TrimSpace(input.ContextKey),
			SourcePostID:  sourcePostID,
			Title:         input.Title,
			Content:       input.Content,
			Summary:       input.Summary,
			CoverURL:      input.CoverURL,
			Visibility:    visibility,
			AllowComments: allowComments,
			ChannelID:     channelID,
			CollectionIDs: strings.Join(collectionIDs, ","),
		}
		upsertCols := []string{"source_post_id", "title", "content", "summary", "cover_url", "visibility", "allow_comments", "channel_id", "collection_ids", "updated_at"}
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "context_key"}},
			DoUpdates: clause.AssignmentColumns(upsertCols),
		}).Create(&draft).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save draft"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": buildBlogDraftResponse(draft), "message": "ok"})
	}
}

// DeleteBlogDraft godoc
// @Summary 删除编辑器草稿
// @Description 按 context_key 删除当前用户保存的编辑器草稿。
// @Tags blog-posts
// @Produce json
// @Param context_key query string true "编辑上下文 key"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/blog/drafts [delete]
func DeleteBlogDraft(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		contextKey := strings.TrimSpace(c.Query("context_key"))
		if contextKey == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "context_key required"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if err := db.Where("user_id = ? AND context_key = ?", userID, contextKey).Delete(&model.BlogDraft{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete draft"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// AddPostToCollection adds a post to a collection
// AddPostToCollection godoc
// @Summary 将文章加入合集
// @Description 为当前用户拥有的文章增加一个同频道合集归属。
// @Tags blog-posts
// @Accept json
// @Produce json
// @Param id path string true "文章 UUID"
// @Param input body CollectionActionInput true "合集操作输入"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/blog/posts/{id}/collections [post]
func AddPostToCollection(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		postID := c.Param("id")
		var input CollectionActionInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var post model.Post
		if err := db.First(&post, "id = ?", postID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Post not found"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if post.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to modify this post"})
			return
		}

		if post.ChannelID == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Post is not assigned to a channel"})
			return
		}

		// Verify collection exists and belongs to user's channel
		var collection model.Collection
		if err := db.Preload("Channel").First(&collection, "id = ?", input.CollectionID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Collection not found"})
			return
		}

		if !ownsChannel(collection.Channel.UserID, userID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to add to this collection"})
			return
		}

		if collection.ChannelID != *post.ChannelID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Collection does not belong to post channel"})
			return
		}

		// Add to collection
		if err := db.Model(&post).Association("Collections").Append(&collection); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add post to collection"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// RemovePostFromCollection removes a post from a collection
// RemovePostFromCollection godoc
// @Summary 将文章移出合集
// @Description 移除当前用户拥有文章与指定合集之间的关联。
// @Tags blog-posts
// @Produce json
// @Param id path string true "文章 UUID"
// @Param collection_id path string true "合集 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/blog/posts/{id}/collections/{collection_id} [delete]
func RemovePostFromCollection(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		postID := c.Param("id")
		collectionID := c.Param("collection_id")

		var post model.Post
		if err := db.First(&post, "id = ?", postID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Post not found"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if post.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to modify this post"})
			return
		}

		if post.ChannelID == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Post is not assigned to a channel"})
			return
		}

		var collection model.Collection
		if err := db.First(&collection, "id = ?", collectionID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Collection not found"})
			return
		}

		if collection.ChannelID != *post.ChannelID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Collection does not belong to post channel"})
			return
		}

		// Remove from collection
		if err := db.Model(&post).Association("Collections").Delete(&collection); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove post from collection"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// Helper functions

func updatePostStatus(c *gin.Context, db *gorm.DB, status string) {
	id := c.Param("id")
	var post model.Post

	if err := db.First(&post, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Post not found"})
		return
	}

	userIDVal, _ := c.Get("user_id")
	userID := userIDVal.(uuid.UUID)

	if post.UserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to modify this post"})
		return
	}

	if err := db.Model(&post).Update("status", status).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update post status"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

func updatePostPin(c *gin.Context, db *gorm.DB, pinned bool) {
	id := c.Param("id")
	var post model.Post

	if err := db.First(&post, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Post not found"})
		return
	}

	userIDVal, _ := c.Get("user_id")
	userID := userIDVal.(uuid.UUID)

	if post.UserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to modify this post"})
		return
	}

	if err := db.Model(&post).Update("pinned", pinned).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update post pin status"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

func currentBlogViewerID(c *gin.Context) *uuid.UUID {
	userIDVal, exists := c.Get("user_id")
	if !exists {
		return nil
	}
	userID, ok := userIDVal.(uuid.UUID)
	if !ok {
		return nil
	}
	return &userID
}

func canViewPublishedBlogPost(db *gorm.DB, viewerID *uuid.UUID, post model.Post) (bool, error) {
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

func buildBlogDraftResponse(draft model.BlogDraft) BlogDraftResponse {
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

	return BlogDraftResponse{
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

func RequireBlogPostEditAccess(db *gorm.DB, h gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		userID, ok := userIDVal.(uuid.UUID)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}

		postID := strings.TrimSpace(c.Param("roomID"))
		if postID == "" {
			postID = strings.TrimSpace(c.Param("id"))
		}
		if postID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing post id"})
			return
		}

		var post model.Post
		if err := db.Select("id", "user_id").First(&post, "id = ?", postID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "Post not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load post"})
			return
		}
		if post.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to edit this post"})
			return
		}

		h(c)
	}
}
