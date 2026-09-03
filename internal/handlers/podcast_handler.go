package handlers

import (
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"atoman/internal/middleware"
)

func SetupPodcastRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	p := router.Group("/api/v1/podcast")
	p.GET("/episodes", middleware.OptionalAuthMiddleware(), GetPodcastEpisodes(db))
	p.GET("/recommend/episodes", GetRecommendedPodcastEpisodes(db))
	p.GET("/shows/:channelSlug/episodes", middleware.OptionalAuthMiddleware(), GetShowEpisodes(db))
	p.GET("/episodes/:id", middleware.OptionalAuthMiddleware(), GetPodcastEpisode(db))
	p.POST("/episodes/:id/playback", RecordPodcastPlayback(db))
	p.GET("/bookmarks", middleware.AuthMiddleware(), GetPodcastEpisodeBookmarks(db))
	p.POST("/bookmarks", middleware.AuthMiddleware(), CreatePodcastEpisodeBookmark(db))
	p.DELETE("/bookmarks/:id", middleware.AuthMiddleware(), DeletePodcastEpisodeBookmark(db))
	p.GET("/show-bookmarks", middleware.AuthMiddleware(), GetPodcastShowBookmarks(db))
	p.POST("/show-bookmarks", middleware.AuthMiddleware(), CreatePodcastShowBookmark(db))
	p.DELETE("/show-bookmarks/:id", middleware.AuthMiddleware(), DeletePodcastShowBookmark(db))
	p.POST("/episodes", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "podcast", "podcast.publish"), CreatePodcastEpisode(db))
	p.PUT("/episodes/:id", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "podcast", "podcast.publish"), UpdatePodcastEpisode(db))
	p.DELETE("/episodes/:id", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "podcast", "podcast.publish"), DeletePodcastEpisode(db))
	p.POST("/upload-audio", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "podcast", "podcast.publish"), UploadPodcastAudio(s3Client))
	p.POST("/upload-cover", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "podcast", "podcast.publish"), UploadPodcastCover(s3Client))
}
