package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/service"
)

// SetupBlogInteractionRoutes configures comment, like, and bookmark routes
func SetupBlogInteractionRoutes(router *gin.Engine, db *gorm.DB) {
	blog := router.Group("/api/v1/blog")
	{
		// Public routes
		blog.GET("/posts/:id/comments", GetPostComments(db))
		blog.POST("/posts/:id/comments", middleware.OptionalAuthMiddleware(), CreateComment(db))
		blog.GET("/posts/:id/likes/count", GetPostLikesCount(db))

		// Protected routes
		protected := blog.Group("")
		protected.Use(middleware.AuthMiddleware())
		{
			protected.DELETE("/comments/:id", DeleteComment(db))

			protected.POST("/likes", ToggleLike(db, true))
			protected.DELETE("/likes", ToggleLike(db, false))

			protected.GET("/bookmarks", GetBookmarks(db))
			protected.POST("/bookmarks", CreateBookmark(db))
			protected.DELETE("/bookmarks/:id", DeleteBookmark(db))

			protected.GET("/bookmark-folders", GetBookmarkFolders(db))
			protected.POST("/bookmark-folders", CreateBookmarkFolder(db))
			protected.DELETE("/bookmark-folders/:id", DeleteBookmarkFolder(db))
		}
	}
}

// CommentInput represents the request body for creating a comment
type CommentInput struct {
	GuestName    string `json:"guest_name"`
	Content      string `json:"content" binding:"required"`
	TimestampSec *int   `json:"timestamp_sec"`
}

func currentCommenterID(c *gin.Context) *uuid.UUID {
	userIDVal, ok := c.Get("user_id")
	if !ok {
		return nil
	}
	userID, ok := userIDVal.(uuid.UUID)
	if !ok {
		return nil
	}
	return &userID
}

func nullableCommentUserID(userID *uuid.UUID) model.NullableUserUUID {
	if userID == nil {
		return model.NullableUserUUID{}
	}
	return model.NewNullableUserUUID(*userID)
}

// LikeInput represents the request body for liking/unliking
type LikeInput struct {
	TargetType string    `json:"target_type" binding:"required,oneof=post comment"`
	TargetID   uuid.UUID `json:"target_id" binding:"required"`
}

// BookmarkInput represents the request body for bookmarking
type BookmarkInput struct {
	PostID           uuid.UUID  `json:"post_id" binding:"required"`
	BookmarkFolderID *uuid.UUID `json:"bookmark_folder_id"`
}

// BookmarkFolderInput represents the request body for creating a bookmark folder
type BookmarkFolderInput struct {
	Name string `json:"name" binding:"required"`
}

func ensureBlogPostInteractive(db *gorm.DB, viewerID *uuid.UUID, post model.Post) (int, string, bool) {
	if post.Status == "draft" {
		if viewerID == nil || post.UserID != *viewerID {
			return http.StatusForbidden, "You don't have permission to interact with this post", false
		}
		return 0, "", true
	}

	allowed, err := canViewPublishedBlogPost(db, viewerID, post)
	if err != nil {
		return http.StatusInternalServerError, "Failed to evaluate post visibility", false
	}
	if !allowed {
		return http.StatusForbidden, "You don't have permission to interact with this post", false
	}

	return 0, "", true
}

// GetPostComments returns all visible comments for a post
// GetPostComments godoc
// @Summary 获取文章评论列表
// @Description 返回指定文章的可见评论列表。
// @Tags blog-interactions
// @Produce json
// @Param id path string true "文章 UUID"
// @Success 200 {object} CommentListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/blog/posts/{id}/comments [get]
func GetPostComments(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		postID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid post id"})
			return
		}
		var post model.Post
		if err := db.First(&post, "id = ?", postID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Post not found"})
			return
		}
		viewerID := currentBlogViewerID(c)
		if status, message, ok := ensureBlogPostInteractive(db, viewerID, post); !ok {
			c.JSON(status, gin.H{"error": message})
			return
		}

		var comments []model.Comment

		if err := db.Preload("User").Where("target_type = ? AND target_id = ? AND status = ?", "post", postID, "visible").Order("created_at ASC").Find(&comments).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch comments"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": comments, "message": "ok"})
	}
}

// CreateComment creates a new comment on a post
// CreateComment godoc
// @Summary 创建文章评论
// @Description 为指定文章创建一条评论。
// @Tags blog-interactions
// @Accept json
// @Produce json
// @Param id path string true "文章 UUID"
// @Param input body CommentInput true "评论输入"
// @Success 201 {object} CommentResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/blog/posts/{id}/comments [post]
func CreateComment(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		postID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid post id"})
			return
		}

		var input CommentInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var post model.Post
		if err := db.Where("id = ?", postID).First(&post).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Post not found"})
			return
		}

		if !post.AllowComments {
			c.JSON(http.StatusForbidden, gin.H{"error": "Comments are disabled for this post"})
			return
		}

		commenterID := currentCommenterID(c)
		if status, message, ok := ensureBlogPostInteractive(db, commenterID, post); !ok {
			c.JSON(status, gin.H{"error": message})
			return
		}

		matrix, err := service.NewSiteAccessService(db).PublicMatrix()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load comment settings"})
			return
		}

		switch matrix.Settings.Blog.CommentMode {
		case service.SiteAccessBlogCommentModeDisabled:
			c.JSON(http.StatusForbidden, gin.H{"error": "Comments are disabled for this site"})
			return
		case service.SiteAccessBlogCommentModeAuth:
			if commenterID == nil {
				c.JSON(http.StatusForbidden, gin.H{"error": "Login required to comment"})
				return
			}
		case service.SiteAccessBlogCommentModeAll:
		default:
			c.JSON(http.StatusForbidden, gin.H{"error": "Comments are disabled for this site"})
			return
		}

		guestName := strings.TrimSpace(input.GuestName)
		if commenterID == nil && guestName == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "guest_name is required"})
			return
		}

		comment := model.Comment{
			TargetType:   "post",
			TargetID:     post.ID,
			UserID:       nullableCommentUserID(commenterID),
			GuestName:    guestName,
			Content:      input.Content,
			TimestampSec: input.TimestampSec,
			Status:       "visible",
		}

		if err := db.Create(&comment).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create comment"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": comment, "message": "ok"})
	}
}

// DeleteComment deletes a comment (by comment owner or post owner)
// DeleteComment godoc
// @Summary 删除评论
// @Description 删除当前用户拥有的评论，或删除自己文章下的评论。
// @Tags blog-interactions
// @Produce json
// @Param id path string true "评论 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/comments/{id} [delete]
func DeleteComment(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid comment id"})
			return
		}

		var comment model.Comment

		if err := db.Where("id = ?", id).First(&comment).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Comment not found"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		// Check if user is comment owner or post owner
		isPostOwner := false
		if comment.TargetType == "post" {
			var post model.Post
			if db.Select("user_id").Where("id = ?", comment.TargetID).First(&post).Error == nil {
				isPostOwner = post.UserID == userID
			}
		}
		if !comment.UserID.Valid {
			if !isPostOwner {
				c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to delete this comment"})
				return
			}
		} else if comment.UserID.UUID != userID && !isPostOwner {
			c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to delete this comment"})
			return
		}

		if err := db.Delete(&comment).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete comment"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// ToggleLike handles liking and unliking
// ToggleLike godoc
// @Summary 点赞或取消点赞
// @Description 通过 POST 创建点赞，通过 DELETE 取消点赞；支持 post 和 comment。
// @Tags blog-interactions
// @Accept json
// @Produce json
// @Param input body LikeInput true "点赞输入"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/likes [post]
// @Router /api/v1/blog/likes [delete]
func ToggleLike(db *gorm.DB, isLike bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input LikeInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if input.TargetType == "post" {
			var post model.Post
			if err := db.First(&post, input.TargetID).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "Post not found"})
				return
			}
			if status, message, ok := ensureBlogPostInteractive(db, &userID, post); !ok {
				c.JSON(status, gin.H{"error": message})
				return
			}
		} else {
			var comment model.Comment
			if err := db.First(&comment, input.TargetID).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "Comment not found"})
				return
			}
		}

		if isLike {
			like := model.Like{
				UserID:     userID,
				TargetType: input.TargetType,
				TargetID:   input.TargetID,
			}

			// Use FirstOrCreate and tolerate duplicate-key races from concurrent requests.
			if err := db.Where(model.Like{UserID: userID, TargetType: input.TargetType, TargetID: input.TargetID}).FirstOrCreate(&like).Error; err != nil {
				if !migrations.IsBlogInteractionDuplicateKeyError(err) {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to like"})
					return
				}
				if err := db.Where(model.Like{UserID: userID, TargetType: input.TargetType, TargetID: input.TargetID}).First(&like).Error; err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to like"})
					return
				}
			}

		} else {
			if err := db.Where("user_id = ? AND target_type = ? AND target_id = ?", userID, input.TargetType, input.TargetID).Delete(&model.Like{}).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unlike"})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// GetPostLikesCount returns the number of likes for a post
// GetPostLikesCount godoc
// @Summary 获取文章点赞数
// @Description 返回指定文章的点赞总数。
// @Tags blog-interactions
// @Produce json
// @Param id path string true "文章 UUID"
// @Success 200 {object} LikeCountResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/blog/posts/{id}/likes/count [get]
func GetPostLikesCount(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		postID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid post id"})
			return
		}
		var count int64

		if err := db.Model(&model.Like{}).Where("target_type = ? AND target_id = ?", "post", postID).Count(&count).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get likes count"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"count": count}, "message": "ok"})
	}
}

// GetBookmarks returns the authenticated user's bookmarks
// GetBookmarks godoc
// @Summary 获取我的书签列表
// @Description 返回当前用户的书签列表，可按书签文件夹过滤。
// @Tags blog-interactions
// @Produce json
// @Param folder_id query string false "书签文件夹 UUID"
// @Success 200 {object} BookmarkListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmarks [get]
func GetBookmarks(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var bookmarks []model.Bookmark
		query := db.Preload("Post").Preload("Post.User").Where("user_id = ?", userID)

		if folderID := c.Query("folder_id"); folderID != "" {
			query = query.Where("bookmark_folder_id = ?", folderID)
		}

		if err := query.Order("created_at DESC").Find(&bookmarks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch bookmarks"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": bookmarks, "message": "ok"})
	}
}

// CreateBookmark creates a new bookmark
// CreateBookmark godoc
// @Summary 创建书签
// @Description 将文章加入当前用户的书签。
// @Tags blog-interactions
// @Accept json
// @Produce json
// @Param input body BookmarkInput true "书签输入"
// @Success 201 {object} BookmarkResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmarks [post]
func CreateBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input BookmarkInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var post model.Post
		if err := db.First(&post, input.PostID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Post not found"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		if status, message, ok := ensureBlogPostInteractive(db, &userID, post); !ok {
			c.JSON(status, gin.H{"error": message})
			return
		}

		if input.BookmarkFolderID != nil {
			var folder model.BookmarkFolder
			if err := db.Where("id = ? AND user_id = ?", *input.BookmarkFolderID, userID).First(&folder).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "Bookmark folder not found or doesn't belong to you"})
				return
			}
		}

		bookmark := model.Bookmark{
			UserID:           userID,
			PostID:           input.PostID,
			BookmarkFolderID: input.BookmarkFolderID,
		}

		if err := db.Where(model.Bookmark{UserID: userID, PostID: input.PostID}).FirstOrCreate(&bookmark).Error; err != nil {
			if !migrations.IsBlogInteractionDuplicateKeyError(err) {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create bookmark"})
				return
			}
			if err := db.Where(model.Bookmark{UserID: userID, PostID: input.PostID}).First(&bookmark).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create bookmark"})
				return
			}
		}

		c.JSON(http.StatusCreated, gin.H{"data": bookmark, "message": "ok"})
	}
}

// DeleteBookmark deletes a bookmark
// DeleteBookmark godoc
// @Summary 删除书签
// @Description 删除当前用户的指定书签。
// @Tags blog-interactions
// @Produce json
// @Param id path string true "书签 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmarks/{id} [delete]
func DeleteBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bookmark id"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if err := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.Bookmark{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete bookmark"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// GetBookmarkFolders returns the authenticated user's bookmark folders
// GetBookmarkFolders godoc
// @Summary 获取书签文件夹列表
// @Description 返回当前用户的书签文件夹列表。
// @Tags blog-interactions
// @Produce json
// @Success 200 {object} BookmarkFolderListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmark-folders [get]
func GetBookmarkFolders(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var folders []model.BookmarkFolder
		if err := db.Where("user_id = ?", userID).Order("created_at DESC").Find(&folders).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch bookmark folders"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": folders, "message": "ok"})
	}
}

// CreateBookmarkFolder creates a new bookmark folder
// CreateBookmarkFolder godoc
// @Summary 创建书签文件夹
// @Description 为当前用户创建一个书签文件夹。
// @Tags blog-interactions
// @Accept json
// @Produce json
// @Param input body BookmarkFolderInput true "书签文件夹输入"
// @Success 201 {object} BookmarkFolderResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmark-folders [post]
func CreateBookmarkFolder(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input BookmarkFolderInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		folder := model.BookmarkFolder{
			UserID: userID,
			Name:   input.Name,
		}

		if err := db.Create(&folder).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create bookmark folder"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": folder, "message": "ok"})
	}
}

// DeleteBookmarkFolder deletes a bookmark folder
// DeleteBookmarkFolder godoc
// @Summary 删除书签文件夹
// @Description 删除当前用户的书签文件夹，并把其中书签移到未分组状态。
// @Tags blog-interactions
// @Produce json
// @Param id path string true "书签文件夹 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmark-folders/{id} [delete]
func DeleteBookmarkFolder(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bookmark folder id"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		// Start transaction to delete folder and update bookmarks
		err = db.Transaction(func(tx *gorm.DB) error {
			// Set folder_id to null for all bookmarks in this folder
			if err := tx.Model(&model.Bookmark{}).Where("bookmark_folder_id = ? AND user_id = ?", id, userID).Update("bookmark_folder_id", nil).Error; err != nil {
				return err
			}

			// Delete the folder
			if err := tx.Where("id = ? AND user_id = ?", id, userID).Delete(&model.BookmarkFolder{}).Error; err != nil {
				return err
			}

			return nil
		})

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete bookmark folder"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}
