package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/service"
)

// SetupBlogChannelRoutes configures blog channel and collection routes
func SetupBlogChannelRoutes(router *gin.Engine, db *gorm.DB) {
	blog := router.Group("/api/v1/blog")
	{
		// Public routes
		blog.GET("/channels", middleware.OptionalAuthMiddleware(), GetChannels(db))
		blog.GET("/channels/:id", middleware.OptionalAuthMiddleware(), GetChannel(db))
		blog.GET("/channels/:id/collections", middleware.OptionalAuthMiddleware(), GetChannelCollections(db))
		blog.GET("/channels/slug/:slug", middleware.OptionalAuthMiddleware(), GetChannelBySlug(db))
		blog.GET("/channels/slug/:slug/collections", middleware.OptionalAuthMiddleware(), GetChannelCollectionsBySlug(db))
		blog.GET("/collections", middleware.OptionalAuthMiddleware(), GetUserCollections(db))
		blog.GET("/collections/:id", middleware.OptionalAuthMiddleware(), GetCollection(db))
		blog.GET("/channels/slug/:slug/rss/article", GetChannelArticleRSS(db))

		// Protected routes
		protected := blog.Group("")
		protected.Use(middleware.AuthMiddleware())
		{
			protected.POST("/channels/ensure-default", EnsureDefaultChannel(db))
			protected.POST("/channels", CreateChannel(db))
			protected.PUT("/channels/:id", UpdateChannel(db))
			protected.DELETE("/channels/:id", DeleteChannel(db))

			protected.POST("/channels/:id/collections", CreateCollection(db))
			protected.PUT("/collections/:id", UpdateCollection(db))
			protected.DELETE("/collections/:id", DeleteCollection(db))
		}
	}
}

// ChannelInput represents the request body for creating/updating a channel
type ChannelInput struct {
	Name        string `json:"name" binding:"required"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
}

// CollectionInput represents the request body for creating/updating a collection
type CollectionInput struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
}

type DeleteChannelInput struct {
	Password        string `json:"password" binding:"required"`
	MoveContent     bool   `json:"move_content"`
	TargetChannelID string `json:"target_channel_id"`
}

func writeChannelSlugError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrSiteHandleReserved) {
		c.JSON(http.StatusConflict, gin.H{"error": "Channel slug is reserved"})
		return
	}
	if errors.Is(err, service.ErrSiteHandleTaken) {
		c.JSON(http.StatusConflict, gin.H{"error": "Channel slug is already in use"})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel slug"})
}

// EnsureDefaultChannel creates a default channel for the authenticated user if they don't have one
// EnsureDefaultChannel godoc
// @Summary 确保默认频道存在
// @Description 为当前用户创建默认频道；若已存在则直接返回。
// @Tags blog-channels
// @Produce json
// @Success 200 {object} ChannelResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/channels/ensure-default [post]
func EnsureDefaultChannel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var user model.User
		if err := db.First(&user, "uuid = ?", userID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}

		channel, err := EnsureDefaultChannelForUser(db, userID, user.Username)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to ensure default channel: " + err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": channel, "message": "ok"})
	}
}

// GetChannels returns a list of channels, optionally filtered by user_id
// GetChannels godoc
// @Summary 获取频道列表
// @Description 返回频道列表，可按用户 UUID 过滤。
// @Tags blog-channels
// @Produce json
// @Param user_id query string false "用户 UUID"
// @Success 200 {object} ChannelListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/blog/channels [get]
func GetChannels(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var channels []model.Channel
		query := db.Preload("User")

		if userID := c.Query("user_id"); userID != "" {
			if _, err := uuid.Parse(userID); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user_id"})
				return
			}
			query = query.Where("user_id = ?", userID)
		}

		if err := query.Find(&channels).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch channels"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": channels, "message": "ok"})
	}
}

// GetChannel returns a specific channel by ID
// GetChannel godoc
// @Summary 获取频道详情
// @Description 按频道 UUID 返回频道详情。
// @Tags blog-channels
// @Produce json
// @Param id path string true "频道 UUID"
// @Success 200 {object} ChannelResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/blog/channels/{id} [get]
func GetChannel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var channel model.Channel

		if err := db.Preload("User").First(&channel, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Channel not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": channel, "message": "ok"})
	}
}

// GetChannelBySlug returns a specific channel by slug
// GetChannelBySlug godoc
// @Summary 按 slug 获取频道详情
// @Description 按频道 slug 返回频道详情。
// @Tags blog-channels
// @Produce json
// @Param slug path string true "频道 slug"
// @Success 200 {object} ChannelResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/blog/channels/slug/{slug} [get]
func GetChannelBySlug(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		slug := strings.TrimSpace(c.Param("slug"))
		var channel model.Channel

		if err := db.Preload("User").First(&channel, "slug = ?", slug).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Channel not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": channel, "message": "ok"})
	}
}

// CreateChannel creates a new channel for the authenticated user
// CreateChannel godoc
// @Summary 创建频道
// @Description 为当前登录用户创建一个频道，并自动生成默认合集。
// @Tags blog-channels
// @Accept json
// @Produce json
// @Param input body ChannelInput true "频道输入"
// @Success 201 {object} ChannelResponse
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/channels [post]
func CreateChannel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input ChannelInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		input.Name = normalizeName(input.Name)
		input.Description = strings.TrimSpace(input.Description)
		if input.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Channel name is required"})
			return
		}

		exists, err := channelNameExists(db, input.Name, nil)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to validate channel name"})
			return
		}
		if exists {
			c.JSON(http.StatusConflict, gin.H{"error": "Channel name already exists"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		slugSource := input.Slug
		if strings.TrimSpace(slugSource) != "" {
			slug, err := validateExplicitChannelSlug(db, slugSource, nil)
			if err != nil {
				writeChannelSlugError(c, err)
				return
			}
			slugSource = slug
		} else {
			slugSource = input.Name
		}
		slug := slugSource
		if strings.TrimSpace(input.Slug) == "" {
			var err error
			slug, err = uniqueChannelSlug(db, slugSource, nil)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate channel slug"})
				return
			}
		}

		channel := model.Channel{
			UserID:      &userID,
			Name:        input.Name,
			Slug:        slug,
			Description: input.Description,
			CoverURL:    input.CoverURL,
		}

		createChannelFailed := false
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&channel).Error; err != nil {
				createChannelFailed = true
				return err
			}
			if _, err := ensureDefaultCollection(tx, channel.ID); err != nil {
				return err
			}
			return nil
		}); err != nil {
			if createChannelFailed {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create channel"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create default collection"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": channel, "message": "ok"})
	}
}

// UpdateChannel updates an existing channel (only by owner)
// UpdateChannel godoc
// @Summary 更新频道
// @Description 更新当前用户拥有的频道信息。
// @Tags blog-channels
// @Accept json
// @Produce json
// @Param id path string true "频道 UUID"
// @Param input body ChannelInput true "频道输入"
// @Success 200 {object} ChannelResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/channels/{id} [put]
func UpdateChannel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var input ChannelInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		input.Name = normalizeName(input.Name)
		input.Description = strings.TrimSpace(input.Description)
		if input.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Channel name is required"})
			return
		}

		var channel model.Channel
		if err := db.First(&channel, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Channel not found"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if !ownsChannel(channel.UserID, userID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to update this channel"})
			return
		}

		excludeID := channel.ID
		exists, err := channelNameExists(db, input.Name, &excludeID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to validate channel name"})
			return
		}
		if exists {
			c.JSON(http.StatusConflict, gin.H{"error": "Channel name already exists"})
			return
		}

		slugSource := input.Slug
		if strings.TrimSpace(slugSource) != "" {
			slug, err := validateExplicitChannelSlug(db, slugSource, &excludeID)
			if err != nil {
				writeChannelSlugError(c, err)
				return
			}
			slugSource = slug
		} else {
			slugSource = input.Name
		}
		slug := slugSource
		if strings.TrimSpace(input.Slug) == "" {
			var err error
			slug, err = uniqueChannelSlug(db, slugSource, &excludeID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate channel slug"})
				return
			}
		}

		if err := db.Model(&channel).Updates(model.Channel{
			Name:        input.Name,
			Slug:        slug,
			Description: input.Description,
			CoverURL:    input.CoverURL,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update channel"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": channel, "message": "ok"})
	}
}

// DeleteChannel deletes a channel (only by owner)
// DeleteChannel godoc
// @Summary 删除频道
// @Description 删除当前用户拥有的频道，可选择是否迁移内容到目标频道。
// @Tags blog-channels
// @Accept json
// @Produce json
// @Param id path string true "频道 UUID"
// @Param input body DeleteChannelInput true "删除频道输入"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/channels/{id} [delete]
func DeleteChannel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var channel model.Channel

		if err := db.First(&channel, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Channel not found"})
			return
		}

		if channel.IsDefault {
			c.JSON(http.StatusForbidden, gin.H{"error": "Cannot delete default channel"})
			return
		}

		var input DeleteChannelInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if !ownsChannel(channel.UserID, userID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to delete this channel"})
			return
		}

		var user model.User
		if err := db.First(&user, "uuid = ?", userID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify user"})
			return
		}

		if input.Password == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Password is required"})
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Password is incorrect"})
			return
		}

		var targetChannel *model.Channel
		if input.MoveContent {
			if strings.TrimSpace(input.TargetChannelID) == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "target_channel_id is required when move_content is true"})
				return
			}

			targetID, err := uuid.Parse(strings.TrimSpace(input.TargetChannelID))
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid target channel UUID"})
				return
			}

			if targetID == channel.ID {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Target channel must be different from source channel"})
				return
			}

			var target model.Channel
			if err := db.First(&target, "id = ?", targetID).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "Target channel not found"})
				return
			}

			if !ownsChannel(target.UserID, userID) {
				c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to move content to this channel"})
				return
			}

			targetChannel = &target
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			var sourceCollections []model.Collection
			if err := tx.Where("channel_id = ?", channel.ID).Find(&sourceCollections).Error; err != nil {
				return err
			}

			sourceCollectionIDs := make([]uuid.UUID, 0, len(sourceCollections))
			for _, collection := range sourceCollections {
				sourceCollectionIDs = append(sourceCollectionIDs, collection.ID)
			}

			if input.MoveContent && targetChannel != nil {
				if err := tx.Model(&model.Post{}).Where("channel_id = ?", channel.ID).Update("channel_id", targetChannel.ID).Error; err != nil {
					return err
				}

				if len(sourceCollectionIDs) > 0 {
					defaultCollection, err := ensureDefaultCollection(tx, targetChannel.ID)
					if err != nil {
						return err
					}

					var postCollections []model.PostCollection
					if err := tx.Where("collection_id IN ?", sourceCollectionIDs).Find(&postCollections).Error; err != nil {
						return err
					}

					seenPosts := make(map[uuid.UUID]bool)
					for _, relation := range postCollections {
						if seenPosts[relation.PostID] {
							continue
						}
						seenPosts[relation.PostID] = true

						postCollection := model.PostCollection{
							PostID:       relation.PostID,
							CollectionID: defaultCollection.ID,
						}

						if err := tx.Where("post_id = ? AND collection_id = ?", relation.PostID, defaultCollection.ID).
							FirstOrCreate(&postCollection).Error; err != nil {
							return err
						}
					}
				}
			} else {
				if err := tx.Model(&model.Post{}).Where("channel_id = ?", channel.ID).Update("channel_id", nil).Error; err != nil {
					return err
				}
			}

			if len(sourceCollectionIDs) > 0 {
				if err := tx.Where("collection_id IN ?", sourceCollectionIDs).Delete(&model.PostCollection{}).Error; err != nil {
					return err
				}
			}

			if err := tx.Where("channel_id = ?", channel.ID).Delete(&model.Collection{}).Error; err != nil {
				return err
			}

			if err := tx.Delete(&channel).Error; err != nil {
				return err
			}

			return nil
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete channel"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// GetCollection returns a single collection by ID
// GetCollection godoc
// @Summary 获取合集详情
// @Description 按合集 UUID 返回合集详情。
// @Tags blog-collections
// @Produce json
// @Param id path string true "合集 UUID"
// @Success 200 {object} CollectionResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/blog/collections/{id} [get]
func GetCollection(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var collection model.Collection

		if err := db.Preload("Channel").First(&collection, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Collection not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": collection, "message": "ok"})
	}
}

// GetChannelCollections returns all collections for a specific channel
// GetChannelCollections godoc
// @Summary 获取频道下的合集列表
// @Description 返回指定频道 UUID 下的所有合集。
// @Tags blog-collections
// @Produce json
// @Param id path string true "频道 UUID"
// @Success 200 {object} CollectionListResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/blog/channels/{id}/collections [get]
func GetChannelCollections(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		channelID := c.Param("id")
		var collections []model.Collection

		if err := db.Where("channel_id = ?", channelID).Find(&collections).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch collections"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": collections, "message": "ok"})
	}
}

// GetChannelCollectionsBySlug returns all collections for a specific channel slug
// GetChannelCollectionsBySlug godoc
// @Summary 按频道 slug 获取合集列表
// @Description 返回指定频道 slug 下的所有合集。
// @Tags blog-collections
// @Produce json
// @Param slug path string true "频道 slug"
// @Success 200 {object} CollectionListResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/blog/channels/slug/{slug}/collections [get]
func GetChannelCollectionsBySlug(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		slug := strings.TrimSpace(c.Param("slug"))
		var channel model.Channel
		if err := db.First(&channel, "slug = ?", slug).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Channel not found"})
			return
		}

		var collections []model.Collection
		if err := db.Where("channel_id = ?", channel.ID).Find(&collections).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch collections"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": collections, "message": "ok"})
	}
}

// GetUserCollections returns all collections for the authenticated user with channel names
// GetUserCollections godoc
// @Summary 获取我的合集列表
// @Description 返回当前登录用户名下所有频道的合集，并附带频道名称。
// @Tags blog-collections
// @Produce json
// @Success 200 {object} UserCollectionListResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/collections [get]
func GetUserCollections(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		userID := userIDVal.(uuid.UUID)

		// Get all channels for this user
		var userChannels []model.Channel
		if err := db.Where("user_id = ?", userID).Find(&userChannels).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch channels"})
			return
		}

		channelIDs := make([]uuid.UUID, len(userChannels))
		channelMap := make(map[uuid.UUID]string)
		for i, ch := range userChannels {
			channelIDs[i] = ch.ID
			channelMap[ch.ID] = ch.Name
		}

		// Get all collections for these channels
		var collections []model.Collection
		if len(channelIDs) > 0 {
			if err := db.Where("channel_id IN ?", channelIDs).Order("created_at DESC").Find(&collections).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch collections"})
				return
			}
		}

		// Add channel_name to each collection
		type CollectionWithChannel struct {
			model.Collection
			ChannelName string `json:"channel_name"`
		}

		result := make([]CollectionWithChannel, len(collections))
		for i, col := range collections {
			result[i] = CollectionWithChannel{
				Collection:  col,
				ChannelName: channelMap[col.ChannelID],
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": result, "message": "ok"})
	}
}

// CreateCollection creates a new collection in a channel (only by channel owner)
// CreateCollection godoc
// @Summary 创建合集
// @Description 在当前用户拥有的频道下创建一个合集。
// @Tags blog-collections
// @Accept json
// @Produce json
// @Param id path string true "频道 UUID"
// @Param input body CollectionInput true "合集输入"
// @Success 201 {object} CollectionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/channels/{id}/collections [post]
func CreateCollection(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		channelIDStr := c.Param("id")
		channelID, err := uuid.Parse(channelIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel UUID"})
			return
		}

		var input CollectionInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		input.Name = normalizeName(input.Name)
		input.Description = strings.TrimSpace(input.Description)
		if input.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Collection name is required"})
			return
		}

		// Verify channel exists and belongs to user
		var channel model.Channel
		if err := db.First(&channel, "id = ?", channelID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Channel not found"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if !ownsChannel(channel.UserID, userID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to add collections to this channel"})
			return
		}

		exists, err := collectionNameExists(db, channelID, input.Name, nil)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to validate collection name"})
			return
		}
		if exists {
			c.JSON(http.StatusConflict, gin.H{"error": "Collection name already exists in this channel"})
			return
		}

		collection := model.Collection{
			ChannelID:   channelID,
			Name:        input.Name,
			Description: input.Description,
			CoverURL:    input.CoverURL,
		}

		if err := db.Create(&collection).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create collection"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": collection, "message": "ok"})
	}
}

// UpdateCollection updates an existing collection (only by channel owner)
// UpdateCollection godoc
// @Summary 更新合集
// @Description 更新当前用户可管理的合集信息。
// @Tags blog-collections
// @Accept json
// @Produce json
// @Param id path string true "合集 UUID"
// @Param input body CollectionInput true "合集输入"
// @Success 200 {object} CollectionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/collections/{id} [put]
func UpdateCollection(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var input CollectionInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		input.Name = normalizeName(input.Name)
		input.Description = strings.TrimSpace(input.Description)
		if input.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Collection name is required"})
			return
		}

		var collection model.Collection
		if err := db.Preload("Channel").First(&collection, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Collection not found"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if !ownsChannel(collection.Channel.UserID, userID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to update this collection"})
			return
		}

		// Prevent renaming default collection
		if collection.IsDefault && input.Name != collection.Name {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot rename default collection"})
			return
		}

		excludeID := collection.ID
		exists, err := collectionNameExists(db, collection.ChannelID, input.Name, &excludeID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to validate collection name"})
			return
		}
		if exists {
			c.JSON(http.StatusConflict, gin.H{"error": "Collection name already exists in this channel"})
			return
		}

		if err := db.Model(&collection).Updates(model.Collection{
			Name:        input.Name,
			Description: input.Description,
			CoverURL:    input.CoverURL,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update collection"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": collection, "message": "ok"})
	}
}

// DeleteCollection deletes a collection (only by channel owner)
// DeleteCollection godoc
// @Summary 删除合集
// @Description 删除当前用户可管理的合集。
// @Tags blog-collections
// @Produce json
// @Param id path string true "合集 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/collections/{id} [delete]
func DeleteCollection(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var collection model.Collection

		if err := db.Preload("Channel").First(&collection, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Collection not found"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if !ownsChannel(collection.Channel.UserID, userID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "You don't have permission to delete this collection"})
			return
		}

		if err := db.Delete(&collection).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete collection"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// GetChannelArticleRSS outputs a standard RSS 2.0 feed for a channel's published articles.
// Route: GET /api/blog/channels/slug/:slug/rss/article
// GetChannelArticleRSS godoc
// @Summary 获取频道文章 RSS
// @Description 输出指定频道已发布文章的 RSS 2.0 XML。
// @Tags blog-channels
// @Produce xml
// @Param slug path string true "频道 slug"
// @Success 200 {string} string "RSS XML"
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/blog/channels/slug/{slug}/rss/article [get]
func GetChannelArticleRSS(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		slug := c.Param("slug")
		var channel model.Channel
		if err := db.Preload("User").Where("slug = ?", slug).First(&channel).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}

		var posts []model.Post
		db.Where("channel_id = ? AND status = ?", channel.ID, "published").
			Preload("User").
			Order("created_at DESC").
			Limit(50).Find(&posts)

		scheme := c.Request.Header.Get("X-Forwarded-Proto")
		if scheme == "" {
			scheme = "https"
		}
		siteURL := fmt.Sprintf("%s://%s", scheme, c.Request.Host)

		c.Header("Content-Type", "application/rss+xml; charset=utf-8")
		c.String(http.StatusOK, buildArticleRSS(channel, posts, siteURL))
	}
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
