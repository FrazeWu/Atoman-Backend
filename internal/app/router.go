package app

import (
	"os"

	"atoman/internal/collab"
	"atoman/internal/handlers"
	"atoman/internal/modules/blog"
	"atoman/internal/modules/debate"
	"atoman/internal/modules/debate_voting"
	"atoman/internal/modules/feed"
	"atoman/internal/modules/forum"
	"atoman/internal/modules/forum_engagement"
	"atoman/internal/modules/forum_moderation"
	"atoman/internal/modules/music"
	"atoman/internal/modules/notification"
	"atoman/internal/modules/subscription"
	"atoman/internal/middleware"
	"atoman/internal/service"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func RegisterV1Routes(
	r *gin.Engine,
	db *gorm.DB,
	emailService *service.EmailService,
	s3Client *s3.S3,
	userHub *collab.UserHub,
	collabHub *collab.Hub,
) {
	group := r.Group("/api/v1")
	blog.RegisterRoutes(group.Group("/blog"), blog.NewService(db))
	subscription.RegisterRoutes(group, subscription.NewService(db))
	feed.RegisterRoutes(group.Group("/feed"), feed.NewService(db))
	notification.RegisterRoutes(group, notification.NewService(db))
	forum.RegisterRoutes(group.Group("/forum"), forum.NewService(db))
	forum_engagement.RegisterRoutes(group.Group("/forum"), forum_engagement.NewService(db))
	forum_moderation.RegisterRoutes(group.Group("/forum/moderation"), forum_moderation.NewService(db))
	debate.RegisterRoutes(group, debate.NewService(db))
	debate_voting.RegisterRoutes(group, debate_voting.NewService(db))
	music.RegisterRoutes(group.Group("/music"), music.NewService(db))

	handlers.SetupAuthRoutes(r, db, emailService)
	handlers.SetupOnboardingRoutes(r, db)
	handlers.SetupUserRoutes(r, db)
	handlers.SetupBlogChannelRoutes(r, db)
	handlers.SetupBlogInteractionRoutes(r, db)
	handlers.SetupBlogUploadRoutes(r, db, s3Client)
	handlers.SetupFeedRoutes(r, db)
	handlers.SetupSongRoutes(r, db, s3Client)
	handlers.SetupAlbumRoutes(r, db, s3Client)
	handlers.SetupArtistRoutes(r, db)
	handlers.SetupArtistWikiRoutes(r, db)
	handlers.SetupCorrectionRoutes(r, db, s3Client)
	handlers.SetupEntryStatusRoutes(r, db)
	handlers.SetupLyricAnnotationRoutes(r, db)
	notifSvc := service.NewNotificationService(db)
	handlers.SetupForumRoutes(r, db, notifSvc, userHub)
	handlers.SetupDMRoutes(r, db, userHub, s3Client)
	handlers.SetupDebateRoutes(r, db)
	handlers.SetupTimelineRoutes(r, db)
	handlers.SetupVideoRoutes(r, db, s3Client)
	handlers.SetupRevisionRoutes(r, db)
	handlers.SetupDiscussionRoutes(r, db)
	handlers.SetupProtectionRoutes(r, db)
	handlers.SetupPodcastRoutes(r, db, s3Client)
	handlers.SetupAdminRoutes(r, db, s3Client)

	r.GET("/ws/user", func(c *gin.Context) {
		userHub.ServeWS(c, os.Getenv("JWT_SECRET"))
	})

	collabGroup := r.Group("/api/v1/collab")
	collabGroup.Use(middleware.AuthMiddleware())
	collabGroup.GET("/ws/:roomID", handlers.RequireBlogPostEditAccess(db, collabHub.ServeWS))
}
