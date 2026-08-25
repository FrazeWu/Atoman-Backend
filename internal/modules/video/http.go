package video

import (
	"atoman/internal/handlers"
	"atoman/internal/middleware"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func RegisterRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	v := router.Group("/api/v1/videos")
	v.GET("", middleware.OptionalAuthMiddleware(), handlers.GetVideos(db))
	v.GET("/recommend/items", handlers.GetRecommendedVideoItems(db))
	v.GET("/subscriptions", middleware.AuthMiddleware(), handlers.GetSubscribedVideos(db))
	v.GET("/collection-bookmarks", middleware.AuthMiddleware(), handlers.GetVideoCollectionBookmarks(db))
	v.GET("/bookmarks", middleware.AuthMiddleware(), handlers.GetVideoBookmarks(db))
	v.GET("/channel-bookmarks", middleware.AuthMiddleware(), handlers.GetChannelBookmarks(db))
	v.POST("/likes", middleware.AuthMiddleware(), handlers.ToggleVideoLike(db, true))
	v.DELETE("/likes", middleware.AuthMiddleware(), handlers.ToggleVideoLike(db, false))
	v.POST("/bookmarks", middleware.AuthMiddleware(), handlers.CreateVideoBookmark(db))
	v.DELETE("/bookmarks/:id", middleware.AuthMiddleware(), handlers.DeleteVideoBookmark(db))
	v.POST("/channel-bookmarks", middleware.AuthMiddleware(), handlers.CreateChannelBookmark(db))
	v.DELETE("/channel-bookmarks/:id", middleware.AuthMiddleware(), handlers.DeleteChannelBookmark(db))
	v.POST("/upload-video", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), handlers.UploadVideoFile(s3Client))
	v.POST("/upload-cover", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), handlers.UploadVideoCover(s3Client))
	v.POST("/:id/reprocess", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), handlers.ReprocessVideo(db))
	v.GET("/:id", middleware.OptionalAuthMiddleware(), handlers.GetVideo(db))
	v.GET("/:id/recommended", handlers.GetRecommendedVideos(db))
	v.POST("/:id/view", handlers.IncrementVideoView(db))
	v.POST("", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), handlers.CreateVideo(db))
	v.PUT("/:id", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), handlers.UpdateVideo(db))
	v.DELETE("/:id", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), handlers.DeleteVideo(db))
	imports := v.Group("")
	imports.Use(middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"))
	handlers.RegisterVideoImportRoutes(imports, db, s3Client)
}
