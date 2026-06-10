package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
)

// SetupDiscussionRoutes registers discussion-related routes
func SetupDiscussionRoutes(router *gin.Engine, db *gorm.DB) {
	discussions := router.Group("/api")
	{
		// Album discussions
		albums := discussions.Group("/albums/:id")
		{
			albums.GET("/discussions", GetAlbumDiscussionsHandler(db))
			albums.GET("/discussions/unread-count", GetAlbumDiscussionUnreadCountHandler(db))
			albums.POST("/discussions", middleware.AuthMiddleware(), CreateAlbumDiscussionHandler(db))
			albums.PUT("/discussions/:discussion_id", middleware.AuthMiddleware(), UpdateAlbumDiscussionHandler(db))
			albums.DELETE("/discussions/:discussion_id", middleware.AuthMiddleware(), DeleteAlbumDiscussionHandler(db))
			albums.POST("/discussions/:discussion_id/reply", middleware.AuthMiddleware(), ReplyToDiscussionHandler(db))
		}

		// Song discussions
		songs := discussions.Group("/songs/:id")
		{
			songs.GET("/discussions", GetSongDiscussionsHandler(db))
			songs.GET("/discussions/unread-count", GetSongDiscussionUnreadCountHandler(db))
			songs.POST("/discussions", middleware.AuthMiddleware(), CreateSongDiscussionHandler(db))
			songs.PUT("/discussions/:discussion_id", middleware.AuthMiddleware(), UpdateSongDiscussionHandler(db))
			songs.DELETE("/discussions/:discussion_id", middleware.AuthMiddleware(), DeleteSongDiscussionHandler(db))
			songs.POST("/discussions/:discussion_id/reply", middleware.AuthMiddleware(), ReplyToDiscussionHandler(db))
		}

		// Admin delete (soft delete any)
		adminDiscussions := discussions.Group("/admin/discussions")
		adminDiscussions.Use(middleware.AuthMiddleware(), middleware.AdminMiddleware(db))
		{
			adminDiscussions.DELETE("/:id", AdminDeleteDiscussionHandler(db))
		}

		// Artist discussions
		artistsDisc := discussions.Group("/artists/:id")
		{
			artistsDisc.GET("/discussions", GetEntityDiscussionsHandler(db, "artist"))
			artistsDisc.POST("/discussions", middleware.AuthMiddleware(), CreateEntityDiscussionHandler(db, "artist"))
			artistsDisc.DELETE("/discussions/:discussion_id", middleware.AuthMiddleware(), DeleteEntityDiscussionHandler(db))
			artistsDisc.POST("/discussions/:discussion_id/reply", middleware.AuthMiddleware(), ReplyToEntityDiscussionHandler(db, "artist"))
		}
	}
}

// GetAlbumDiscussionUnreadCountHandler godoc
// @Summary 获取专辑未读讨论数
// @Description 返回专辑下未读且未删除的讨论数量。
// @Tags music-discussions
// @Produce json
// @Param id path string true "专辑 UUID"
// @Success 200 {object} DiscussionUnreadCountResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/albums/{id}/discussions/unread-count [get]
// GetAlbumDiscussionUnreadCountHandler returns the count of unread discussions for an album
func GetAlbumDiscussionUnreadCountHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		albumID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid album ID"})
			return
		}

		var count int64
		if err := db.Where("content_type = ? AND content_id = ? AND status != ? AND read_at IS NULL",
			"album", albumID, "deleted").
			Model(&model.Discussion{}).
			Count(&count).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count unread discussions"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"unread_count": count,
			},
		})
	}
}

// GetSongDiscussionUnreadCountHandler godoc
// @Summary 获取歌曲未读讨论数
// @Description 返回歌曲下未读且未删除的讨论数量。
// @Tags music-discussions
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Success 200 {object} DiscussionUnreadCountResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/songs/{id}/discussions/unread-count [get]
// GetSongDiscussionUnreadCountHandler returns the count of unread discussions for a song
func GetSongDiscussionUnreadCountHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		songID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid song ID"})
			return
		}

		var count int64
		if err := db.Where("content_type = ? AND content_id = ? AND status != ? AND read_at IS NULL",
			"song", songID, "deleted").
			Model(&model.Discussion{}).
			Count(&count).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count unread discussions"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"unread_count": count,
			},
		})
	}
}

// GetAlbumDiscussionsHandler godoc
// @Summary 获取专辑讨论列表
// @Description 分页返回专辑的顶层讨论及其已预加载的回复。
// @Tags music-discussions
// @Produce json
// @Param id path string true "专辑 UUID"
// @Param limit query int false "返回数量"
// @Param offset query int false "偏移量"
// @Success 200 {object} DiscussionListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/albums/{id}/discussions [get]
// GetAlbumDiscussionsHandler returns discussions for an album
func GetAlbumDiscussionsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		albumID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid album ID"})
			return
		}

		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

		var discussions []model.Discussion
		var total int64

		query := db.Where("content_type = ? AND content_id = ? AND parent_id IS NULL AND status != ?",
			"album", albumID, "deleted")

		if err := query.Model(&model.Discussion{}).Count(&total).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count discussions"})
			return
		}

		if err := query.
			Preload("User").
			Preload("Replies.User").
			Preload("Replies.Replies.User").
			Order("created_at DESC").
			Limit(limit).
			Offset(offset).
			Find(&discussions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch discussions"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data":  discussions,
			"total": total,
		})
	}
}

// GetSongDiscussionsHandler godoc
// @Summary 获取歌曲讨论列表
// @Description 分页返回歌曲的顶层讨论及其已预加载的回复。
// @Tags music-discussions
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Param limit query int false "返回数量"
// @Param offset query int false "偏移量"
// @Success 200 {object} DiscussionListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/songs/{id}/discussions [get]
// GetSongDiscussionsHandler returns discussions for a song
func GetSongDiscussionsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		songID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid song ID"})
			return
		}

		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

		var discussions []model.Discussion
		var total int64

		query := db.Where("content_type = ? AND content_id = ? AND parent_id IS NULL AND status != ?",
			"song", songID, "deleted")

		query.Model(&model.Discussion{}).Count(&total)

		if err := query.
			Preload("User").
			Preload("Replies.User").
			Order("created_at DESC").
			Limit(limit).
			Offset(offset).
			Find(&discussions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch discussions"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data":  discussions,
			"total": total,
		})
	}
}

type CreateDiscussionInput struct {
	Content  string     `json:"content" binding:"required"`
	ParentID *uuid.UUID `json:"parent_id"`
}

// CreateAlbumDiscussionHandler godoc
// @Summary 创建专辑讨论
// @Description 为专辑创建一条讨论，支持可选 parent_id 作为嵌套回复。
// @Tags music-discussions
// @Accept json
// @Produce json
// @Param id path string true "专辑 UUID"
// @Param input body CreateDiscussionInput true "讨论输入"
// @Success 201 {object} DiscussionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/albums/{id}/discussions [post]
// CreateAlbumDiscussionHandler creates a new discussion for an album
func CreateAlbumDiscussionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		albumID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid album ID"})
			return
		}

		var input CreateDiscussionInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Content is required"})
			return
		}

		userID := c.GetString("user_id")
		userUUID, _ := uuid.Parse(userID)

		discussion := model.Discussion{
			ContentType: "album",
			ContentID:   albumID,
			UserID:      userUUID,
			Content:     input.Content,
			ParentID:    input.ParentID,
			Status:      "active",
		}

		if err := db.Create(&discussion).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create discussion"})
			return
		}

		// Reload with user
		db.Preload("User").First(&discussion, discussion.ID)

		c.JSON(http.StatusCreated, gin.H{"data": discussion})
	}
}

// CreateSongDiscussionHandler godoc
// @Summary 创建歌曲讨论
// @Description 为歌曲创建一条讨论，支持可选 parent_id 作为嵌套回复。
// @Tags music-discussions
// @Accept json
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Param input body CreateDiscussionInput true "讨论输入"
// @Success 201 {object} DiscussionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/songs/{id}/discussions [post]
// CreateSongDiscussionHandler creates a new discussion for a song
func CreateSongDiscussionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		songID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid song ID"})
			return
		}

		var input CreateDiscussionInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Content is required"})
			return
		}

		userID := c.GetString("user_id")
		userUUID, _ := uuid.Parse(userID)

		discussion := model.Discussion{
			ContentType: "song",
			ContentID:   songID,
			UserID:      userUUID,
			Content:     input.Content,
			ParentID:    input.ParentID,
			Status:      "active",
		}

		if err := db.Create(&discussion).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create discussion"})
			return
		}

		db.Preload("User").First(&discussion, discussion.ID)

		c.JSON(http.StatusCreated, gin.H{"data": discussion})
	}
}

// ReplyToDiscussionHandler godoc
// @Summary 回复专辑或歌曲讨论
// @Description 为现有专辑或歌曲讨论创建一条回复。
// @Tags music-discussions
// @Accept json
// @Produce json
// @Param id path string true "内容 UUID"
// @Param discussion_id path string true "讨论 UUID"
// @Param input body DiscussionContentInput true "回复内容"
// @Success 201 {object} DiscussionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/albums/{id}/discussions/{discussion_id}/reply [post]
// @Router /api/v1/songs/{id}/discussions/{discussion_id}/reply [post]
// ReplyToDiscussionHandler adds a reply to an existing discussion
func ReplyToDiscussionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		discussionID, err := uuid.Parse(c.Param("discussion_id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid discussion ID"})
			return
		}

		// Verify parent discussion exists
		var parent model.Discussion
		if err := db.First(&parent, "id = ?", discussionID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Parent discussion not found"})
			return
		}

		var input struct {
			Content string `json:"content" binding:"required"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Content is required"})
			return
		}

		userID := c.GetString("user_id")
		userUUID, _ := uuid.Parse(userID)

		reply := model.Discussion{
			ContentType: parent.ContentType,
			ContentID:   parent.ContentID,
			UserID:      userUUID,
			Content:     input.Content,
			ParentID:    &parent.ID,
			Status:      "active",
		}

		if err := db.Create(&reply).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create reply"})
			return
		}

		db.Preload("User").First(&reply, reply.ID)

		c.JSON(http.StatusCreated, gin.H{"data": reply})
	}
}

// UpdateAlbumDiscussionHandler godoc
// @Summary 更新专辑讨论
// @Description 讨论所有者或管理员可以更新专辑讨论内容。
// @Tags music-discussions
// @Accept json
// @Produce json
// @Param id path string true "专辑 UUID"
// @Param discussion_id path string true "讨论 UUID"
// @Param input body DiscussionContentInput true "讨论内容"
// @Success 200 {object} DiscussionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/albums/{id}/discussions/{discussion_id} [put]
// UpdateAlbumDiscussionHandler updates a discussion (only by owner)
func UpdateAlbumDiscussionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		discussionID, err := uuid.Parse(c.Param("discussion_id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid discussion ID"})
			return
		}

		var discussion model.Discussion
		if err := db.First(&discussion, "id = ?", discussionID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Discussion not found"})
			return
		}

		// Check ownership
		userID := c.GetString("user_id")
		userRole := c.GetString("role")
		if discussion.UserID.String() != userID && userRole != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "You can only edit your own discussions"})
			return
		}

		var input struct {
			Content string `json:"content" binding:"required"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Content is required"})
			return
		}

		if err := db.Model(&discussion).Update("content", input.Content).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update discussion"})
			return
		}

		db.Preload("User").First(&discussion, discussion.ID)

		c.JSON(http.StatusOK, gin.H{"data": discussion})
	}
}

// UpdateSongDiscussionHandler updates a discussion for song
// UpdateSongDiscussionHandler godoc
// @Summary 更新歌曲讨论
// @Description 讨论所有者或管理员可以更新歌曲讨论内容。
// @Tags music-discussions
// @Accept json
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Param discussion_id path string true "讨论 UUID"
// @Param input body DiscussionContentInput true "讨论内容"
// @Success 200 {object} DiscussionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/songs/{id}/discussions/{discussion_id} [put]
func UpdateSongDiscussionHandler(db *gorm.DB) gin.HandlerFunc {
	return UpdateAlbumDiscussionHandler(db) // Same logic
}

// DeleteAlbumDiscussionHandler godoc
// @Summary 删除专辑讨论
// @Description 讨论所有者或管理员可将专辑讨论软删除。
// @Tags music-discussions
// @Produce json
// @Param id path string true "专辑 UUID"
// @Param discussion_id path string true "讨论 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/albums/{id}/discussions/{discussion_id} [delete]
// DeleteAlbumDiscussionHandler soft deletes a discussion (only by owner or admin)
func DeleteAlbumDiscussionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		discussionID, err := uuid.Parse(c.Param("discussion_id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid discussion ID"})
			return
		}

		var discussion model.Discussion
		if err := db.First(&discussion, "id = ?", discussionID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Discussion not found"})
			return
		}

		userID := c.GetString("user_id")
		userRole := c.GetString("role")
		if discussion.UserID.String() != userID && userRole != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "You can only delete your own discussions"})
			return
		}

		if err := db.Model(&discussion).Update("status", "deleted").Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete discussion"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Discussion deleted"})
	}
}

// DeleteSongDiscussionHandler godoc
// @Summary 删除歌曲讨论
// @Description 讨论所有者或管理员可将歌曲讨论软删除。
// @Tags music-discussions
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Param discussion_id path string true "讨论 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/songs/{id}/discussions/{discussion_id} [delete]
// DeleteSongDiscussionHandler soft deletes a discussion for song
func DeleteSongDiscussionHandler(db *gorm.DB) gin.HandlerFunc {
	return DeleteAlbumDiscussionHandler(db) // Same logic
}

// AdminDeleteDiscussionHandler admin can delete any discussion
// AdminDeleteDiscussionHandler godoc
// @Summary 管理员删除讨论
// @Description 管理员可软删除任意讨论。
// @Tags music-discussions
// @Produce json
// @Param id path string true "讨论 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/discussions/{id} [delete]
func AdminDeleteDiscussionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		discussionID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid discussion ID"})
			return
		}

		var discussion model.Discussion
		if err := db.First(&discussion, "id = ?", discussionID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Discussion not found"})
			return
		}

		if err := db.Model(&discussion).Update("status", "deleted").Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete discussion"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Discussion deleted by admin"})
	}
}

// GetEntityDiscussionsHandler godoc
// @Summary 获取艺人讨论列表
// @Description 分页返回艺人条目的顶层讨论及其回复。
// @Tags music-discussions
// @Produce json
// @Param id path string true "艺人 UUID"
// @Param limit query int false "返回数量"
// @Param offset query int false "偏移量"
// @Success 200 {object} DiscussionListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/artists/{id}/discussions [get]
// GetEntityDiscussionsHandler returns discussions for any content type (artist, album, etc.)
func GetEntityDiscussionsHandler(db *gorm.DB, contentType string) gin.HandlerFunc {
	return func(c *gin.Context) {
		entityID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
			return
		}
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

		var discussions []model.Discussion
		var total int64

		query := db.Where("content_type = ? AND content_id = ? AND parent_id IS NULL AND status != ?",
			contentType, entityID, "deleted")
		query.Model(&model.Discussion{}).Count(&total)

		if err := query.
			Preload("User").
			Preload("Replies.User").
			Order("created_at DESC").
			Limit(limit).Offset(offset).
			Find(&discussions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch discussions"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": discussions, "total": total})
	}
}

// CreateEntityDiscussionHandler godoc
// @Summary 创建艺人讨论
// @Description 为艺人条目创建一条讨论，支持可选 parent_id 作为嵌套回复。
// @Tags music-discussions
// @Accept json
// @Produce json
// @Param id path string true "艺人 UUID"
// @Param input body CreateDiscussionInput true "讨论输入"
// @Success 201 {object} DiscussionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/artists/{id}/discussions [post]
// CreateEntityDiscussionHandler creates a discussion for any content type
func CreateEntityDiscussionHandler(db *gorm.DB, contentType string) gin.HandlerFunc {
	return func(c *gin.Context) {
		entityID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
			return
		}
		var input CreateDiscussionInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Content is required"})
			return
		}
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		discussion := model.Discussion{
			ContentType: contentType,
			ContentID:   entityID,
			UserID:      userID,
			Content:     input.Content,
			ParentID:    input.ParentID,
			Status:      "active",
		}
		if err := db.Create(&discussion).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create discussion"})
			return
		}
		db.Preload("User").First(&discussion, "id = ?", discussion.ID)
		c.JSON(http.StatusCreated, gin.H{"data": discussion})
	}
}

// DeleteEntityDiscussionHandler soft-deletes a discussion (owner or admin)
// DeleteEntityDiscussionHandler godoc
// @Summary 删除艺人讨论
// @Description 讨论所有者或管理员可将艺人讨论软删除。
// @Tags music-discussions
// @Produce json
// @Param id path string true "艺人 UUID"
// @Param discussion_id path string true "讨论 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/artists/{id}/discussions/{discussion_id} [delete]
func DeleteEntityDiscussionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		discussionID, err := uuid.Parse(c.Param("discussion_id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid discussion ID"})
			return
		}
		var discussion model.Discussion
		if err := db.First(&discussion, "id = ?", discussionID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Discussion not found"})
			return
		}
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		userRole, _ := c.Get("role")
		if discussion.UserID != userID && userRole != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden"})
			return
		}
		db.Model(&discussion).Update("status", "deleted")
		c.JSON(http.StatusOK, gin.H{"message": "Discussion deleted"})
	}
}

// ReplyToEntityDiscussionHandler adds a reply to any entity discussion
// ReplyToEntityDiscussionHandler godoc
// @Summary 回复艺人讨论
// @Description 为艺人条目下的现有讨论创建一条回复。
// @Tags music-discussions
// @Accept json
// @Produce json
// @Param id path string true "艺人 UUID"
// @Param discussion_id path string true "讨论 UUID"
// @Param input body DiscussionContentInput true "回复内容"
// @Success 201 {object} DiscussionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/artists/{id}/discussions/{discussion_id}/reply [post]
func ReplyToEntityDiscussionHandler(db *gorm.DB, contentType string) gin.HandlerFunc {
	return func(c *gin.Context) {
		entityID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
			return
		}
		discussionID, err := uuid.Parse(c.Param("discussion_id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid discussion ID"})
			return
		}
		var input struct {
			Content string `json:"content" binding:"required"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Content is required"})
			return
		}
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		reply := model.Discussion{
			ContentType: contentType,
			ContentID:   entityID,
			UserID:      userID,
			Content:     input.Content,
			ParentID:    &discussionID,
			Status:      "active",
		}
		if err := db.Create(&reply).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create reply"})
			return
		}
		db.Preload("User").First(&reply, "id = ?", reply.ID)
		c.JSON(http.StatusCreated, gin.H{"data": reply})
	}
}
