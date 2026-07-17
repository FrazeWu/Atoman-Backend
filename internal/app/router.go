package app

import (
	"os"

	"atoman/internal/collab"
	"atoman/internal/handlers"
	"atoman/internal/middleware"
	"atoman/internal/modules/blog"
	"atoman/internal/modules/comment"
	"atoman/internal/modules/debate"
	"atoman/internal/modules/debate_voting"
	"atoman/internal/modules/feed"
	"atoman/internal/modules/forum"
	"atoman/internal/modules/forum_engagement"
	"atoman/internal/modules/forum_moderation"
	"atoman/internal/modules/music"
	"atoman/internal/modules/notification"
	"atoman/internal/modules/portal"
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
	commentService := comment.NewService(db, comment.NewTargetRegistry(db))
	forumService := forum.NewService(db)
	commentService.SetForumPolicy(forumService)
	comment.RegisterRoutes(group, commentService)
	blog.RegisterRoutes(group.Group("/blog"), blog.NewService(db))
	feed.RegisterRoutes(group.Group("/feed"), feed.NewService(db))
	notification.RegisterRoutes(group, notification.NewService(db))
	forumGroup := group.Group("/forum")
	forum.RegisterRoutes(forumGroup, forumService)
	forum_engagement.RegisterRoutes(forumGroup, forum_engagement.NewService(db))
	forumModerationService := forum_moderation.NewService(db)
	forum_moderation.RegisterLegacyRoutes(forumGroup, forumModerationService)
	forum_moderation.RegisterRoutes(forumGroup.Group("/moderation"), forumModerationService)
	debate.RegisterRoutes(group, debate.NewService(db, commentService))
	debate_voting.RegisterRoutes(group, debate_voting.NewService(db))
	musicGroup := group.Group("/music")
	musicGroup.Use(middleware.OptionalAuthMiddleware())
	music.RegisterRoutes(musicGroup, music.NewServiceWithS3(db, s3Client))
	portal.RegisterRoutes(group.Group("/portal"), portal.NewService(db))

	handlers.SetupAuthRoutes(r, db, emailService)
	handlers.SetupSiteRoutes(r, db)
	handlers.SetupOnboardingRoutes(r, db)
	handlers.SetupUserRoutes(r, db)
	handlers.SetupBlogUploadRoutes(r, db, s3Client)
	handlers.SetupUploadRoutes(r, db, s3Client)
	handlers.SetupSongRoutes(r, db, s3Client)
	handlers.SetupAlbumRoutes(r, db, s3Client)
	handlers.SetupArtistRoutes(r, db)
	handlers.SetupArtistWikiRoutes(r, db)
	handlers.SetupCorrectionRoutes(r, db, s3Client)
	handlers.SetupEntryStatusRoutes(r, db)
	handlers.SetupDMRoutes(r, db, userHub, s3Client)
	handlers.SetupTimelineRoutes(r, db)
	handlers.SetupVideoRoutes(r, db, s3Client)
	handlers.SetupRevisionRoutes(r, db)
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
