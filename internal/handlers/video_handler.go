package handlers

import (
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"atoman/internal/middleware"
)

func SetupVideoRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	v := router.Group("/api/v1/videos")
	{
		v.GET("", middleware.OptionalAuthMiddleware(), GetVideos(db))
		v.GET("/recommend/items", GetRecommendedVideoItems(db))
		v.POST("/:id/reprocess", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), ReprocessVideo(db))
		v.GET("/:id", GetVideo(db))
		v.GET("/:id/recommended", GetRecommendedVideos(db))
		v.POST("/:id/view", IncrementVideoView(db))
		v.POST("", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), CreateVideo(db))
		v.PUT("/:id", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), UpdateVideo(db))
		v.DELETE("/:id", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), DeleteVideo(db))
		v.GET("/bookmarks", middleware.AuthMiddleware(), GetVideoBookmarks(db))
		v.POST("/bookmarks", middleware.AuthMiddleware(), CreateVideoBookmark(db))
		v.DELETE("/bookmarks/:id", middleware.AuthMiddleware(), DeleteVideoBookmark(db))
		v.GET("/channel-bookmarks", middleware.AuthMiddleware(), GetChannelBookmarks(db))
		v.POST("/channel-bookmarks", middleware.AuthMiddleware(), CreateChannelBookmark(db))
		v.DELETE("/channel-bookmarks/:id", middleware.AuthMiddleware(), DeleteChannelBookmark(db))
		// File upload endpoints
		v.POST("/upload-video", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), UploadVideoFile(s3Client))
		v.POST("/upload-cover", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), UploadVideoCover(s3Client))
		imports := v.Group("")
		imports.Use(middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"))
		registerVideoImportRoutes(imports, db, s3Client)
	}
	// Per-channel Video RSS feed
	router.GET("/api/v1/channels/slug/:slug/rss/video", GetVideoRSS(db))
}
