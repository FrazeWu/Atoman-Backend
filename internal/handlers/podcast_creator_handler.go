package handlers

import (
	"net/http"
	"strings"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type podcastCreatorDashboard struct {
	TotalEpisodes     int64                  `json:"total_episodes"`
	PublishedEpisodes int64                  `json:"published_episodes"`
	DraftEpisodes     int64                  `json:"draft_episodes"`
	TotalComments     int64                  `json:"total_comments"`
	TotalBookmarks    int64                  `json:"total_bookmarks"`
	TotalListenLater  int64                  `json:"total_listen_later"`
	RecentEpisodes    []model.PodcastEpisode `json:"recent_episodes"`
	Issues            []string               `json:"issues"`
}

type podcastCreatorComment struct {
	ID           uuid.UUID   `json:"id"`
	TargetType   string      `json:"target_type"`
	TargetID     uuid.UUID   `json:"target_id"`
	UserID       uuid.UUID   `json:"user_id"`
	User         *model.User `json:"user,omitempty"`
	Content      string      `json:"content"`
	TimestampSec *int        `json:"timestamp_sec,omitempty"`
	Status       string      `json:"status"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

func SetupPodcastCreatorRoutes(group *gin.RouterGroup, db *gorm.DB) {
	creator := group.Group("/creator")
	creator.Use(middleware.AuthMiddleware())
	creator.GET("/dashboard", GetPodcastCreatorDashboard(db))
	creator.GET("/episodes", GetPodcastCreatorEpisodes(db))
	creator.GET("/analytics", GetPodcastCreatorAnalytics(db))
	creator.GET("/comments", GetPodcastCreatorComments(db))
	creator.GET("/settings", unsupportedPodcastCreatorSettings)
	creator.PUT("/settings", unsupportedPodcastCreatorSettings)
}

func creatorUserID(c *gin.Context) uuid.UUID {
	value, _ := c.Get("user_id")
	userID, _ := value.(uuid.UUID)
	return userID
}

func creatorEpisodeQuery(db *gorm.DB, userID uuid.UUID) *gorm.DB {
	return db.Model(&model.PodcastEpisode{}).
		Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.user_id = ? AND posts.deleted_at IS NULL", userID)
}

func GetPodcastCreatorDashboard(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := creatorUserID(c)
		var result podcastCreatorDashboard
		creatorEpisodeQuery(db, userID).Count(&result.TotalEpisodes)
		creatorEpisodeQuery(db, userID).Where("posts.status = ?", "published").Count(&result.PublishedEpisodes)
		creatorEpisodeQuery(db, userID).Where("posts.status = ?", "draft").Count(&result.DraftEpisodes)
		creatorEpisodeQuery(db, userID).
			Joins("JOIN discussion_targets ON discussion_targets.kind = ? AND discussion_targets.resource_id = podcast_episodes.id", "podcast_episode").
			Joins("JOIN comment_entries ON comment_entries.target_id = discussion_targets.id AND comment_entries.deleted_at IS NULL").
			Count(&result.TotalComments)
		creatorEpisodeQuery(db, userID).
			Joins("JOIN podcast_episode_bookmarks ON podcast_episode_bookmarks.episode_id = podcast_episodes.id AND podcast_episode_bookmarks.deleted_at IS NULL").
			Count(&result.TotalBookmarks)
		result.RecentEpisodes = make([]model.PodcastEpisode, 0)
		db.Preload("Post").Preload("Channel").
			Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.user_id = ? AND posts.deleted_at IS NULL", userID).
			Order("podcast_episodes.created_at DESC").Limit(5).Find(&result.RecentEpisodes)
		result.Issues = []string{}
		c.JSON(http.StatusOK, gin.H{"data": result})
	}
}

func GetPodcastCreatorEpisodes(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := db.Preload("Post.Collection").Preload("Channel").
			Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.user_id = ? AND posts.deleted_at IS NULL", creatorUserID(c)).
			Order("podcast_episodes.created_at DESC")
		if status := strings.TrimSpace(c.Query("status")); status != "" {
			query = query.Where("posts.status = ?", status)
		}
		if visibility := strings.TrimSpace(c.Query("visibility")); visibility != "" {
			query = query.Where("posts.visibility = ?", visibility)
		}
		var episodes []model.PodcastEpisode
		if err := query.Find(&episodes).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load creator episodes"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": episodes})
	}
}

func GetPodcastCreatorAnalytics(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := creatorUserID(c)
		data := map[string]int64{}
		var episodes, comments, bookmarks int64
		creatorEpisodeQuery(db, userID).Count(&episodes)
		creatorEpisodeQuery(db, userID).
			Joins("JOIN discussion_targets ON discussion_targets.kind = ? AND discussion_targets.resource_id = podcast_episodes.id", "podcast_episode").
			Joins("JOIN comment_entries ON comment_entries.target_id = discussion_targets.id AND comment_entries.deleted_at IS NULL").
			Count(&comments)
		creatorEpisodeQuery(db, userID).
			Joins("JOIN podcast_episode_bookmarks ON podcast_episode_bookmarks.episode_id = podcast_episodes.id AND podcast_episode_bookmarks.deleted_at IS NULL").
			Count(&bookmarks)
		data["episodes"], data["comments"], data["bookmarks"] = episodes, comments, bookmarks
		c.JSON(http.StatusOK, gin.H{"data": data})
	}
}

func GetPodcastCreatorComments(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := db.Table("comment_entries AS comments").
			Select("comments.id, comments.content, comments.status, comments.created_at, comments.updated_at, comments.author_id AS user_id, targets.resource_id AS target_id").
			Joins("JOIN discussion_targets AS targets ON targets.id = comments.target_id AND targets.kind = ?", "podcast_episode").
			Joins("JOIN podcast_episodes ON podcast_episodes.id = targets.resource_id").
			Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.user_id = ? AND posts.deleted_at IS NULL", creatorUserID(c)).
			Where("comments.deleted_at IS NULL").Order("comments.created_at DESC")
		if c.Query("timestamped") == "true" {
			query = query.Joins("JOIN comment_time_anchors ON comment_time_anchors.comment_id = comments.id")
		}
		var rows []podcastCreatorComment
		if err := query.Find(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load creator comments"})
			return
		}
		for i := range rows {
			var user model.User
			if err := db.First(&user, "uuid = ?", rows[i].UserID).Error; err == nil {
				rows[i].User = &user
			}
			var anchor model.CommentTimeAnchor
			if db.Where("comment_id = ?", rows[i].ID).Order("seconds ASC").First(&anchor).Error == nil {
				rows[i].TimestampSec = &anchor.Seconds
			}
			rows[i].TargetType = "podcast_episode"
		}
		c.JSON(http.StatusOK, gin.H{"data": rows})
	}
}

func unsupportedPodcastCreatorSettings(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"error": "podcast creator settings are not supported"})
}
