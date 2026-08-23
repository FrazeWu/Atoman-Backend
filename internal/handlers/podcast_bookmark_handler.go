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
		query := db.Where("podcast_episode_bookmarks.user_id = ?", userID)

		if c.Query("kind") != "" {
			query = query.Where("podcast_episode_bookmarks.kind = ?", kind)
		}
		if sort == "popular" {
			query = query.
				Joins("JOIN content_episode_extensions AS episodes ON episodes.episode_id = podcast_episode_bookmarks.episode_id").
				Order("episodes.view_count DESC").
				Order("podcast_episode_bookmarks.created_at DESC")
		} else {
			query = query.Order("podcast_episode_bookmarks.created_at DESC")
		}
		if err := query.Find(&bookmarks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch podcast bookmarks"})
			return
		}
		if err := hydratePodcastBookmarks(db, bookmarks); err != nil {
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

		if _, err := contentmodule.LoadPodcastEpisode(db, contentmodule.PodcastQuery(db).Where("episodes.episode_id = ?", input.EpisodeID)); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}

		bookmark := model.PodcastEpisodeBookmark{UserID: userID, EpisodeID: input.EpisodeID, Kind: kind}
		if err := db.Where(model.PodcastEpisodeBookmark{UserID: userID, EpisodeID: input.EpisodeID, Kind: kind}).FirstOrCreate(&bookmark).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create podcast bookmark"})
			return
		}
		if err := db.First(&bookmark, "id = ?", bookmark.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create podcast bookmark"})
			return
		}
		hydrated := []model.PodcastEpisodeBookmark{bookmark}
		if err := hydratePodcastBookmarks(db, hydrated); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create podcast bookmark"})
			return
		}
		bookmark = hydrated[0]
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

func hydratePodcastBookmarks(db *gorm.DB, bookmarks []model.PodcastEpisodeBookmark) error {
	if len(bookmarks) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(bookmarks))
	for _, bookmark := range bookmarks {
		ids = append(ids, bookmark.EpisodeID)
	}
	episodes, err := contentmodule.LoadPodcastEpisodes(db, contentmodule.PodcastQuery(db).Where("episodes.episode_id IN ?", ids))
	if err != nil {
		return err
	}
	byID := make(map[uuid.UUID]*model.PodcastEpisode, len(episodes))
	for index := range episodes {
		episode := episodes[index]
		byID[episode.ID] = &episode
	}
	for index := range bookmarks {
		bookmarks[index].Episode = byID[bookmarks[index].EpisodeID]
	}
	return nil
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
