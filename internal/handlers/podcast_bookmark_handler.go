package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
)

type podcastEpisodeBookmarkInput struct {
	EpisodeID uuid.UUID `json:"episode_id" binding:"required"`
	Kind      string    `json:"kind"`
}

func normalizePodcastEpisodeBookmarkKind(kind string) (string, bool) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "favorite", true
	}
	return kind, kind == "favorite" || kind == "listen_later"
}

type podcastShowBookmarkInput struct {
	ChannelID uuid.UUID `json:"channel_id" binding:"required"`
}

func GetPodcastEpisodeBookmarks(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		sort := strings.TrimSpace(c.DefaultQuery("sort", "latest"))
		kind, valid := normalizePodcastEpisodeBookmarkKind(c.Query("kind"))
		if c.Query("kind") != "" && !valid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid podcast bookmark kind"})
			return
		}
		var bookmarks []model.PodcastEpisodeBookmark
		query := db.Preload("Episode").Preload("Episode.Post").Preload("Episode.Channel").Where("podcast_episode_bookmarks.user_id = ?", userID)
		if c.Query("kind") != "" {
			query = query.Where("podcast_episode_bookmarks.kind = ?", kind)
		}
		if sort == "popular" {
			query = query.
				Joins("JOIN podcast_episodes ON podcast_episodes.id = podcast_episode_bookmarks.episode_id").
				Joins("JOIN posts ON posts.id = podcast_episodes.post_id").
				Order("COALESCE(posts.view_count, 0) DESC").
				Order("podcast_episode_bookmarks.created_at DESC")
		} else {
			query = query.Order("podcast_episode_bookmarks.created_at DESC")
		}
		if err := query.Find(&bookmarks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch podcast bookmarks"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": bookmarks, "message": "ok"})
	}
}

func CreatePodcastEpisodeBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		var input podcastEpisodeBookmarkInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		kind, valid := normalizePodcastEpisodeBookmarkKind(input.Kind)
		if !valid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid podcast bookmark kind"})
			return
		}

		var episode model.PodcastEpisode
		if err := db.First(&episode, "id = ?", input.EpisodeID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}

		bookmark := model.PodcastEpisodeBookmark{UserID: userID, EpisodeID: input.EpisodeID, Kind: kind}
		if err := db.Where(model.PodcastEpisodeBookmark{UserID: userID, EpisodeID: input.EpisodeID, Kind: kind}).FirstOrCreate(&bookmark).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create podcast bookmark"})
			return
		}
		if err := db.Preload("Episode").Preload("Episode.Post").Preload("Episode.Channel").First(&bookmark, "id = ?", bookmark.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create podcast bookmark"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": bookmark, "message": "ok"})
	}
}

func DeletePodcastEpisodeBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bookmark id"})
			return
		}
		if err := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.PodcastEpisodeBookmark{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete podcast bookmark"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

func GetPodcastShowBookmarks(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		sort := strings.TrimSpace(c.DefaultQuery("sort", "latest"))
		var bookmarks []model.ChannelBookmark
		query := db.Preload("Channel").Where("user_id = ? AND kind = ?", userID, "podcast_show")
		if sort == "popular" {
			query = query.Order("channel_bookmarks.created_at DESC")
		} else {
			query = query.Order("channel_bookmarks.created_at DESC")
		}
		if err := query.Find(&bookmarks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch podcast show bookmarks"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": bookmarks, "message": "ok"})
	}
}

func CreatePodcastShowBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		var input podcastShowBookmarkInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var channel model.Channel
		if err := db.First(&channel, "id = ?", input.ChannelID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "show not found"})
			return
		}

		bookmark := model.ChannelBookmark{UserID: userID, ChannelID: input.ChannelID, Kind: "podcast_show"}
		if err := db.Where(model.ChannelBookmark{UserID: userID, ChannelID: input.ChannelID, Kind: "podcast_show"}).FirstOrCreate(&bookmark).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create podcast show bookmark"})
			return
		}
		if err := db.Preload("Channel").First(&bookmark, "id = ?", bookmark.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create podcast show bookmark"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": bookmark, "message": "ok"})
	}
}

func DeletePodcastShowBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bookmark id"})
			return
		}
		if err := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.ChannelBookmark{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete podcast show bookmark"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}
