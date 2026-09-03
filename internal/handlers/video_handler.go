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
		v.GET("/recommend/items", middleware.OptionalAuthMiddleware(), GetRecommendedVideoItems(db))
		v.POST("/recommendation-feedback", middleware.AuthMiddleware(), CreateVideoRecommendationFeedback(db))
		v.DELETE("/recommendation-feedback/:scope/:id", middleware.AuthMiddleware(), DeleteVideoRecommendationFeedback(db))
		v.POST("/:id/reprocess", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), ReprocessVideo(db))
		v.POST("/:id/duplicate", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), DuplicateVideo(db))
		v.PUT("/:id/rating", middleware.AuthMiddleware(), SetVideoRating(db))
		v.DELETE("/:id/rating", middleware.AuthMiddleware(), DeleteVideoRating(db))
		v.GET("/bookmarks", middleware.AuthMiddleware(), GetVideoBookmarks(db))
		v.GET("/channel-bookmarks", middleware.AuthMiddleware(), GetChannelBookmarks(db))
		v.POST("/likes", middleware.AuthMiddleware(), ToggleVideoLike(db, true))
		v.DELETE("/likes", middleware.AuthMiddleware(), ToggleVideoLike(db, false))
		v.POST("/bookmarks", middleware.AuthMiddleware(), CreateVideoBookmark(db))
		v.DELETE("/bookmarks/:id", middleware.AuthMiddleware(), DeleteVideoBookmark(db))
		v.POST("/channel-bookmarks", middleware.AuthMiddleware(), CreateChannelBookmark(db))
		v.DELETE("/channel-bookmarks/:id", middleware.AuthMiddleware(), DeleteChannelBookmark(db))
		v.POST("/upload-video", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), UploadVideoFile(s3Client))
		v.POST("/upload-cover", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), UploadVideoCover(s3Client))
		v.GET("/:id", middleware.OptionalAuthMiddleware(), GetVideo(db))
		v.GET("/:id/recommended", GetRecommendedVideos(db))
		v.POST("/:id/view", IncrementVideoView(db))
		v.POST("", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), CreateVideo(db))
		v.PUT("/:id", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), UpdateVideo(db))
		v.DELETE("/:id", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), DeleteVideo(db))
		// File upload endpoints are registered above the parameterized routes.
		imports := v.Group("")
		imports.Use(middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"))
		RegisterVideoImportRoutes(imports, db, s3Client)
	}
}
