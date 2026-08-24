package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	contentmodule "atoman/internal/modules/content"
)

type videoBookmarkInput struct {
	VideoID uuid.UUID `json:"video_id" binding:"required"`
}

type channelBookmarkInput struct {
	ChannelID uuid.UUID `json:"channel_id" binding:"required"`
}

func GetVideoBookmarks(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		sort := strings.TrimSpace(c.DefaultQuery("sort", "latest"))
		var bookmarks []model.VideoBookmark
		query := db.Where("video_bookmarks.user_id = ?", userID)
		if sort == "popular" {
			query = query.
				Joins("JOIN content_video_extensions AS videos ON videos.video_id = video_bookmarks.video_id").
				Order("videos.view_count DESC").
				Order("video_bookmarks.created_at DESC")
		} else {
			query = query.Order("video_bookmarks.created_at DESC")
		}
		if err := query.Find(&bookmarks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch video bookmarks"})
			return
		}
		if err := hydrateVideoBookmarks(db, bookmarks); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch video bookmarks"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": bookmarks, "message": "ok"})
	}
}

func CreateVideoBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		var input videoBookmarkInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if _, err := contentmodule.LoadVideo(db, contentmodule.VideoQuery(db).Where("videos.video_id = ?", input.VideoID)); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}

		bookmark := model.VideoBookmark{UserID: userID, VideoID: input.VideoID}
		if err := db.Where(model.VideoBookmark{UserID: userID, VideoID: input.VideoID}).FirstOrCreate(&bookmark).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create video bookmark"})
			return
		}
		if err := db.First(&bookmark, "id = ?", bookmark.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create video bookmark"})
			return
		}
		hydrated := []model.VideoBookmark{bookmark}
		if err := hydrateVideoBookmarks(db, hydrated); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create video bookmark"})
			return
		}
		bookmark = hydrated[0]
		c.JSON(http.StatusCreated, gin.H{"data": bookmark, "message": "ok"})
	}
}

func DeleteVideoBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bookmark id"})
			return
		}
		if err := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.VideoBookmark{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete video bookmark"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

func hydrateVideoBookmarks(db *gorm.DB, bookmarks []model.VideoBookmark) error {
	if len(bookmarks) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(bookmarks))
	for _, bookmark := range bookmarks {
		ids = append(ids, bookmark.VideoID)
	}
	videos, err := contentmodule.LoadVideos(db, contentmodule.VideoQuery(db).Where("videos.video_id IN ?", ids))
	if err != nil {
		return err
	}
	byID := make(map[uuid.UUID]*model.Video, len(videos))
	for index := range videos {
		video := videos[index]
		byID[video.ID] = &video
	}
	for index := range bookmarks {
		bookmarks[index].Video = byID[bookmarks[index].VideoID]
	}
	return nil
}

func GetChannelBookmarks(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		sort := strings.TrimSpace(c.DefaultQuery("sort", "latest"))
		var bookmarks []model.ChannelBookmark
		query := db.Preload("Channel").Where("user_id = ? AND kind = ?", userID, "video_channel")
		if sort == "popular" {
			query = query.Order("channel_bookmarks.created_at DESC")
		} else {
			query = query.Order("channel_bookmarks.created_at DESC")
		}
		if err := query.Find(&bookmarks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch channel bookmarks"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": bookmarks, "message": "ok"})
	}
}

func CreateChannelBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		var input channelBookmarkInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var channel model.Channel
		if err := db.First(&channel, "id = ?", input.ChannelID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}

		bookmark := model.ChannelBookmark{UserID: userID, ChannelID: input.ChannelID, Kind: "video_channel"}
		if err := db.Where(model.ChannelBookmark{UserID: userID, ChannelID: input.ChannelID, Kind: "video_channel"}).FirstOrCreate(&bookmark).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create channel bookmark"})
			return
		}
		if err := db.Preload("Channel").First(&bookmark, "id = ?", bookmark.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create channel bookmark"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": bookmark, "message": "ok"})
	}
}

func DeleteChannelBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bookmark id"})
			return
		}
		if err := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.ChannelBookmark{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete channel bookmark"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// attachVideoTags upserts VideoTag rows and links them to the video.
