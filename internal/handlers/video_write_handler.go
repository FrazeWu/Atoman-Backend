package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/modules/lifecycle"
	studioapi "atoman/internal/modules/studio"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"atoman/internal/service"
)

// ReprocessVideo godoc
// @Summary 重新处理视频预览
// @Tags videos
// @Produce json
// @Param id path string true "视频 UUID"
// @Success 200 {object} map[string]bool
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Router /api/v1/videos/{id}/reprocess [post]
func ReprocessVideo(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		var video model.Video
		if err := db.First(&video, "id = ?", c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		if video.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		if err := service.EnsureVideoPreviewJob(db, &video); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reprocess video"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

// CreateVideo godoc
// @Summary 创建视频
// @Description 创建一条新视频记录并关联标签与合集。
// @Tags videos
// @Accept json
// @Produce json
// @Param input body VideoCreateInput true "视频创建输入"
// @Success 201 {object} model.Video
// @Failure 400 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos [post]
func CreateVideo(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		var input struct {
			ChannelID     *uuid.UUID  `json:"channel_id"`
			Title         string      `json:"title" binding:"required"`
			Description   string      `json:"description"`
			StorageType   string      `json:"storage_type"`
			VideoURL      string      `json:"video_url" binding:"required"`
			ThumbnailURL  string      `json:"thumbnail_url"`
			DurationSec   int         `json:"duration_sec"`
			Visibility    string      `json:"visibility"`
			Status        string      `json:"status"`
			Tags          []string    `json:"tags"`
			CollectionIDs []uuid.UUID `json:"collection_ids"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		storageType := input.StorageType
		if storageType == "" {
			storageType = "external"
		}
		visibility := input.Visibility
		if visibility == "" {
			visibility = "public"
		}
		status := input.Status
		if status == "" {
			status = "draft"
		}

		if input.ChannelID != nil {
			var channel model.Channel
			if err := db.First(&channel, "id = ?", *input.ChannelID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if !ownsChannel(channel.UserID, userID) {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
		}
		var channelID uuid.UUID
		if input.ChannelID != nil {
			channelID = *input.ChannelID
		}
		if err := studioapi.NewService(db).ValidateContentScope(userID, channelID, studioapi.ModuleVideo, input.CollectionIDs, status == "published"); err != nil {
			httpx.Error(c, err)
			return
		}

		video := model.Video{
			UserID:       userID,
			ChannelID:    input.ChannelID,
			Title:        strings.TrimSpace(input.Title),
			Description:  input.Description,
			StorageType:  storageType,
			VideoURL:     input.VideoURL,
			ThumbnailURL: input.ThumbnailURL,
			DurationSec:  input.DurationSec,
			Visibility:   visibility,
			Status:       status,
		}

		statusCode := http.StatusInternalServerError
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&video).Error; err != nil {
				return err
			}

			if err := service.EnsureVideoPreviewJob(tx, &video); err != nil {
				return fmt.Errorf("processing job failed: %w", err)
			}

			if len(input.Tags) > 0 {
				if err := attachVideoTags(tx, &video, input.Tags); err != nil {
					return fmt.Errorf("tags failed: %w", err)
				}
			}

			if len(input.CollectionIDs) > 0 {
				if err := assignVideoCollections(tx, &video, input.CollectionIDs); err != nil {
					statusCode = http.StatusBadRequest
					return err
				}
			}
			if status == "published" {
				lifecycleService := lifecycle.NewService(tx)
				if err := lifecycleService.ValidatePublishable("video", video.ID); err != nil {
					return err
				}
				return lifecycleService.EnqueuePublication("video", video.ID)
			}
			return nil
		}); err != nil {
			if apperr.FromError(err) != nil {
				httpx.Error(c, err)
				return
			}
			c.JSON(statusCode, gin.H{"error": err.Error()})
			return
		}

		db.Preload("Channel").Preload("Tags").Preload("Collections").First(&video, "id = ?", video.ID)
		c.JSON(http.StatusCreated, video)
	}
}

// UpdateVideo updates a video's fields.
// UpdateVideo godoc
// @Summary 更新视频
// @Description 更新当前用户拥有的视频。
// @Tags videos
// @Accept json
// @Produce json
// @Param id path string true "视频 UUID"
// @Param input body VideoUpdateInput true "视频更新输入"
// @Success 200 {object} model.Video
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/{id} [put]
func UpdateVideo(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		id := c.Param("id")

		var video model.Video
		if err := db.Preload("Collections").First(&video, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		if video.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}

		var input struct {
			ChannelID     *uuid.UUID  `json:"channel_id"`
			Title         *string     `json:"title"`
			Description   *string     `json:"description"`
			ThumbnailURL  *string     `json:"thumbnail_url"`
			Visibility    *string     `json:"visibility"`
			Status        *string     `json:"status"`
			Tags          []string    `json:"tags"`
			CollectionIDs []uuid.UUID `json:"collection_ids"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if input.ChannelID != nil {
			var channel model.Channel
			if err := db.First(&channel, "id = ?", *input.ChannelID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if !ownsChannel(channel.UserID, userID) {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
		}
		effectiveChannelID := uuid.Nil
		if video.ChannelID != nil {
			effectiveChannelID = *video.ChannelID
		}
		if input.ChannelID != nil {
			effectiveChannelID = *input.ChannelID
		}
		effectiveStatus := video.Status
		wasPublished := video.Status == "published"
		if input.Status != nil {
			effectiveStatus = *input.Status
		}
		effectiveCollectionIDs := input.CollectionIDs
		if input.CollectionIDs == nil {
			effectiveCollectionIDs = make([]uuid.UUID, 0, len(video.Collections))
			for _, collection := range video.Collections {
				effectiveCollectionIDs = append(effectiveCollectionIDs, collection.ID)
			}
		}
		if err := studioapi.NewService(db).ValidateContentScope(userID, effectiveChannelID, studioapi.ModuleVideo, effectiveCollectionIDs, effectiveStatus == "published"); err != nil {
			httpx.Error(c, err)
			return
		}

		updates := map[string]interface{}{}
		if input.ChannelID != nil {
			updates["channel_id"] = *input.ChannelID
		}
		if input.Title != nil {
			updates["title"] = strings.TrimSpace(*input.Title)
		}
		if input.Description != nil {
			updates["description"] = *input.Description
		}
		if input.ThumbnailURL != nil {
			updates["thumbnail_url"] = *input.ThumbnailURL
		}
		if input.Visibility != nil {
			updates["visibility"] = *input.Visibility
		}
		if input.Status != nil {
			updates["status"] = *input.Status
		}

		statusCode := http.StatusInternalServerError
		if err := db.Transaction(func(tx *gorm.DB) error {
			if len(updates) > 0 {
				if err := tx.Model(&video).Updates(updates).Error; err != nil {
					return err
				}
				if input.ChannelID != nil {
					video.ChannelID = input.ChannelID
				}
			}

			if input.Tags != nil {
				if err := tx.Model(&video).Association("Tags").Unscoped().Clear(); err != nil {
					return err
				}
				if len(input.Tags) > 0 {
					if err := attachVideoTags(tx, &video, input.Tags); err != nil {
						return err
					}
				}
			}

			if input.CollectionIDs != nil {
				if len(input.CollectionIDs) == 0 {
					if err := tx.Model(&video).Association("Collections").Clear(); err != nil {
						return err
					}
				} else {
					if err := assignVideoCollections(tx, &video, input.CollectionIDs); err != nil {
						statusCode = http.StatusBadRequest
						return err
					}
				}
			}
			if effectiveStatus == "published" && !wasPublished {
				lifecycleService := lifecycle.NewService(tx)
				if err := lifecycleService.ValidatePublishable("video", video.ID); err != nil {
					return err
				}
				return lifecycleService.EnqueuePublication("video", video.ID)
			}
			return nil
		}); err != nil {
			if apperr.FromError(err) != nil {
				httpx.Error(c, err)
				return
			}
			c.JSON(statusCode, gin.H{"error": err.Error()})
			return
		}

		db.Preload("Channel").Preload("Tags").Preload("Collections").First(&video, "id = ?", video.ID)
		c.JSON(http.StatusOK, video)
	}
}

// DeleteVideo soft-deletes a video.
// DeleteVideo godoc
// @Summary 删除视频
// @Description 软删除当前用户拥有的视频。
// @Tags videos
// @Produce json
// @Param id path string true "视频 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/{id} [delete]
func DeleteVideo(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		id := c.Param("id")

		var video model.Video
		if err := db.First(&video, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		if video.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}

		db.Delete(&video)
		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	}
}

func attachVideoTags(db *gorm.DB, video *model.Video, names []string) error {
	var tags []model.VideoTag
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var tag model.VideoTag
		db.Where("name = ?", name).FirstOrCreate(&tag, model.VideoTag{Name: name})
		tags = append(tags, tag)
	}
	return db.Model(video).Association("Tags").Append(tags)
}

func assignVideoCollections(db *gorm.DB, video *model.Video, ids []uuid.UUID) error {
	if video.ChannelID == nil {
		return fmt.Errorf("请选择频道后再关联合集")
	}

	var collections []model.Collection
	if err := db.Where("id IN ? AND channel_id = ?", ids, *video.ChannelID).Find(&collections).Error; err != nil {
		return err
	}
	if len(collections) != len(ids) {
		return fmt.Errorf("存在无效合集或合集不属于当前频道")
	}

	return db.Model(video).Association("Collections").Replace(collections)
}

// GetRecommendedVideos returns up to 8 recommended videos based on same channel (score 60) and same tags (score 40).
