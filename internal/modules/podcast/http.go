package podcast

import (
	"atoman/internal/handlers"
	"atoman/internal/middleware"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func RegisterRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	p := router.Group("/api/v1/podcast")
	p.GET("/episodes", middleware.OptionalAuthMiddleware(), handlers.GetPodcastEpisodes(db))
	p.GET("/recommend/episodes", handlers.GetRecommendedPodcastEpisodes(db))
	p.GET("/shows/:channelSlug/episodes", middleware.OptionalAuthMiddleware(), handlers.GetShowEpisodes(db))
	p.GET("/episodes/:id", middleware.OptionalAuthMiddleware(), handlers.GetPodcastEpisode(db))
	p.POST("/episodes/:id/playback", handlers.RecordPodcastPlayback(db))
	p.GET("/bookmarks", middleware.AuthMiddleware(), handlers.GetPodcastEpisodeBookmarks(db))
	p.POST("/bookmarks", middleware.AuthMiddleware(), handlers.CreatePodcastEpisodeBookmark(db))
	p.DELETE("/bookmarks/:id", middleware.AuthMiddleware(), handlers.DeletePodcastEpisodeBookmark(db))
	p.GET("/show-bookmarks", middleware.AuthMiddleware(), handlers.GetPodcastShowBookmarks(db))
	p.POST("/show-bookmarks", middleware.AuthMiddleware(), handlers.CreatePodcastShowBookmark(db))
	p.DELETE("/show-bookmarks/:id", middleware.AuthMiddleware(), handlers.DeletePodcastShowBookmark(db))
	p.POST("/episodes", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "podcast", "podcast.publish"), handlers.CreatePodcastEpisode(db))
	p.PUT("/episodes/:id", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "podcast", "podcast.publish"), handlers.UpdatePodcastEpisode(db))
	p.DELETE("/episodes/:id", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "podcast", "podcast.publish"), handlers.DeletePodcastEpisode(db))
	p.POST("/upload-audio", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "podcast", "podcast.publish"), handlers.UploadPodcastAudio(s3Client))
	p.POST("/upload-cover", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "podcast", "podcast.publish"), handlers.UploadPodcastCover(s3Client))
}
