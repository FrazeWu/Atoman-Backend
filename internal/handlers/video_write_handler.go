package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"atoman/internal/model"
	contentmodule "atoman/internal/modules/content"
	"atoman/internal/modules/lifecycle"
	studioapi "atoman/internal/modules/studio"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"atoman/internal/platform/indexnow"
	"atoman/internal/service"
)

type videoCreateParams struct {
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
	CollectionID  *uuid.UUID  `json:"collection_id"`
	CollectionIDs []uuid.UUID `json:"collection_ids"`
}

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
		videoID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		video, err := contentmodule.LoadVideo(db, contentmodule.VideoQuery(db).Where("videos.video_id = ?", videoID))
		if err != nil {
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
		var input videoCreateParams
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		video, statusCode, err := createVideoRecord(db, userID, input)
		if err != nil {
			if apperr.FromError(err) != nil {
				httpx.Error(c, err)
				return
			}
			c.JSON(statusCode, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, video)
	}
}

func createVideoRecord(db *gorm.DB, userID uuid.UUID, input videoCreateParams) (model.Video, int, error) {
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
	if input.ChannelID == nil || *input.ChannelID == uuid.Nil {
		return model.Video{}, http.StatusBadRequest, apperr.BadRequest("validation.invalid_request", "channel_id is required")
	}
	channelID := *input.ChannelID
	var channel model.Channel
	if err := db.First(&channel, "id = ?", channelID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Video{}, http.StatusNotFound, apperr.NotFound("video.channel_not_found", "Channel not found")
		}
		return model.Video{}, http.StatusInternalServerError, err
	}
	if !ownsChannel(channel.UserID, userID) {
		return model.Video{}, http.StatusForbidden, apperr.Forbidden("video.channel_forbidden", "Forbidden")
	}
	collectionID, err := studioapi.NewService(db).ResolveContentCollection(
		userID, channelID, studioapi.ModuleVideo, input.CollectionID, input.CollectionIDs, status == "published",
	)
	if err != nil {
		return model.Video{}, http.StatusBadRequest, err
	}
	videoID, err := uuid.NewV7()
	if err != nil {
		return model.Video{}, http.StatusInternalServerError, err
	}
	contentID, err := uuid.NewV7()
	if err != nil {
		return model.Video{}, http.StatusInternalServerError, err
	}
	video := model.Video{
		Base: model.Base{ID: videoID}, UserID: userID, ChannelID: &channelID, CollectionID: collectionID,
		Title: strings.TrimSpace(input.Title), Description: input.Description, StorageType: storageType,
		VideoURL: input.VideoURL, ThumbnailURL: input.ThumbnailURL, DurationSec: input.DurationSec,
		Visibility: visibility, Status: status,
	}
	statusCode := http.StatusInternalServerError
	err = db.Transaction(func(tx *gorm.DB) error {
		entry := model.ContentEntry{
			Base: model.Base{ID: contentID}, AuthorID: &userID, ChannelID: channelID,
			Kind: "video", Title: video.Title, Summary: video.Description, CoverURL: video.ThumbnailURL,
			Status: video.Status, Visibility: video.Visibility,
		}
		if err := tx.Create(&entry).Error; err != nil {
			return err
		}
		extension := model.ContentVideoExtension{
			ContentID: contentID, VideoID: video.ID, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt, StorageType: video.StorageType,
			VideoURL: video.VideoURL, ThumbnailURL: video.ThumbnailURL, DurationSec: video.DurationSec,
			ProcessingStatus: "none",
		}
		if err := tx.Create(&extension).Error; err != nil {
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
		if collectionID != nil {
			if err := tx.Create(&model.ContentCollectionMembership{ContentID: contentID, CollectionID: *collectionID}).Error; err != nil {
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
	})
	if err != nil {
		return model.Video{}, statusCode, err
	}
	video, err = contentmodule.LoadVideo(db, contentmodule.VideoQuery(db).Where("videos.video_id = ?", video.ID))
	if err != nil {
		return model.Video{}, http.StatusInternalServerError, err
	}
	return video, http.StatusCreated, nil
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
		videoID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		video, err := contentmodule.LoadVideo(db, contentmodule.VideoQuery(db).Where("videos.video_id = ?", videoID))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		if video.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		contentID, err := contentmodule.VideoContentID(db, videoID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		wasPublic := video.Status == "published" && video.Visibility == "public"

		var input struct {
			ChannelID     *uuid.UUID        `json:"channel_id"`
			Title         *string           `json:"title"`
			Description   *string           `json:"description"`
			ThumbnailURL  *string           `json:"thumbnail_url"`
			Visibility    *string           `json:"visibility"`
			Status        *string           `json:"status"`
			Tags          []string          `json:"tags"`
			CollectionID  nullableUUIDInput `json:"collection_id"`
			CollectionIDs []uuid.UUID       `json:"collection_ids"`
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
			if video.ChannelID != nil && *input.ChannelID != *video.ChannelID {
				httpx.Error(c, apperr.BadRequest("studio.cross_channel_move_not_supported", "Content cannot be moved between channels"))
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
		if input.Status != nil {
			effectiveStatus = *input.Status
		}
		wasPublished := video.Status == "published"
		collectionChanged := input.CollectionID.Set || input.CollectionIDs != nil
		shouldResolveCollection := collectionChanged || (!wasPublished && effectiveStatus == "published")
		resolvedCollectionID := video.CollectionID
		if shouldResolveCollection {
			if video.CollectionConflict && !collectionChanged {
				httpx.Error(c, apperr.Conflict("studio.collection_conflict", "Choose one collection before publishing"))
				return
			}
			requestedCollectionID := video.CollectionID
			legacyCollectionIDs := []uuid.UUID(nil)
			if collectionChanged {
				requestedCollectionID = nil
				if input.CollectionID.Set {
					requestedCollectionID = input.CollectionID.Value
				}
				legacyCollectionIDs = input.CollectionIDs
			}
			resolvedCollectionID, err = studioapi.NewService(db).ResolveContentCollection(
				userID, effectiveChannelID, studioapi.ModuleVideo, requestedCollectionID, legacyCollectionIDs, effectiveStatus == "published",
			)
			if err != nil {
				httpx.Error(c, err)
				return
			}
		}

		entryUpdates := map[string]any{}
		if input.ChannelID != nil {
			entryUpdates["channel_id"] = *input.ChannelID
		}
		if input.Title != nil {
			entryUpdates["title"] = strings.TrimSpace(*input.Title)
		}
		if input.Description != nil {
			entryUpdates["summary"] = *input.Description
		}
		if input.ThumbnailURL != nil {
			entryUpdates["cover_url"] = *input.ThumbnailURL
		}
		if input.Visibility != nil {
			if *input.Visibility != "public" && *input.Visibility != "followers" && *input.Visibility != "private" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid visibility"})
				return
			}
			entryUpdates["visibility"] = *input.Visibility
		}
		if input.Status != nil {
			entryUpdates["status"] = *input.Status
		}
		extensionUpdates := map[string]any{}
		if input.ThumbnailURL != nil {
			extensionUpdates["thumbnail_url"] = *input.ThumbnailURL
		}
		if shouldResolveCollection {
			extensionUpdates["collection_conflict"] = false
		}

		statusCode := http.StatusInternalServerError
		if err := db.Transaction(func(tx *gorm.DB) error {
			if len(entryUpdates) > 0 {
				if err := tx.Model(&model.ContentEntry{}).Where("id = ?", contentID).Updates(entryUpdates).Error; err != nil {
					return err
				}
			}
			if len(extensionUpdates) > 0 {
				if err := tx.Model(&model.ContentVideoExtension{}).Where("content_id = ?", contentID).Updates(extensionUpdates).Error; err != nil {
					return err
				}
			}
			if input.Tags != nil {
				if err := tx.Where("video_id = ?", videoID).Delete(&model.VideoTagRelation{}).Error; err != nil {
					return err
				}
				if err := attachVideoTags(tx, &video, input.Tags); err != nil {
					return err
				}
			}
			if shouldResolveCollection {
				if err := tx.Where("content_id = ?", contentID).Delete(&model.ContentCollectionMembership{}).Error; err != nil {
					return err
				}
				if resolvedCollectionID != nil {
					if err := tx.Create(&model.ContentCollectionMembership{ContentID: contentID, CollectionID: *resolvedCollectionID}).Error; err != nil {
						statusCode = http.StatusBadRequest
						return err
					}
				}
			}
			if effectiveStatus == "published" && !wasPublished {
				lifecycleService := lifecycle.NewService(tx)
				if err := lifecycleService.ValidatePublishable("video", videoID); err != nil {
					return err
				}
				return lifecycleService.EnqueuePublication("video", videoID)
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
		video, err = contentmodule.LoadVideo(db, contentmodule.VideoQuery(db).Where("videos.video_id = ?", videoID))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if wasPublic || (video.Status == "published" && video.Visibility == "public") {
			indexnow.NotifyPaths("/videos/watch/" + video.ID.String())
		}
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
		videoID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		video, err := contentmodule.LoadVideo(db, contentmodule.VideoQuery(db).Where("videos.video_id = ?", videoID))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		if video.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		wasPublic := video.Status == "published" && video.Visibility == "public"
		contentID, err := contentmodule.VideoContentID(db, videoID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("content_id = ?", contentID).Delete(&model.ContentCollectionMembership{}).Error; err != nil {
				return err
			}
			return tx.Delete(&model.ContentEntry{}, "id = ?", contentID).Error
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if wasPublic {
			indexnow.NotifyPaths("/videos/watch/" + video.ID.String())
		}
		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	}
}

func attachVideoTags(db *gorm.DB, video *model.Video, names []string) error {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var tag model.VideoTag
		if err := db.Where("name = ?", name).FirstOrCreate(&tag, model.VideoTag{Name: name}).Error; err != nil {
			return err
		}
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.VideoTagRelation{VideoID: video.ID, TagID: tag.ID}).Error; err != nil {
			return err
		}
	}
	return nil
}

// GetRecommendedVideos returns up to 8 recommended videos based on same channel (score 60) and same tags (score 40).
