package app

import (
	"net/http"
	"os"
	"strings"
	"time"

	"atoman/internal/collab"
	"atoman/internal/handlers"
	"atoman/internal/middleware"
	"atoman/internal/modules/blog"
	"atoman/internal/modules/comment"
	"atoman/internal/modules/debate"
	"atoman/internal/modules/debate_voting"
	"atoman/internal/modules/dm"
	"atoman/internal/modules/feed"
	"atoman/internal/modules/forum"
	"atoman/internal/modules/forum_engagement"
	"atoman/internal/modules/forum_moderation"
	"atoman/internal/modules/lifecycle"
	"atoman/internal/modules/music"
	"atoman/internal/modules/notification"
	"atoman/internal/modules/podcast"
	"atoman/internal/modules/portal"
	"atoman/internal/modules/reference"
	"atoman/internal/modules/shortnote"
	"atoman/internal/modules/studio"
	"atoman/internal/modules/timeline"
	"atoman/internal/modules/video"
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
	commentService.SetNotificationPublisher(handlers.WsPushNotif(userHub))
	forumService := forum.NewServiceWithComments(db, commentService)
	commentService.SetForumPolicy(forumService)
	comment.RegisterRoutes(group, commentService)
	refService := reference.NewService(db)
	reference.RegisterRoutes(group, refService)
	blog.RegisterRoutes(group.Group("/blog"), blog.NewService(db))
	shortnote.RegisterRoutes(group.Group("/short-notes"), shortnote.NewService(db, refService))
	feed.RegisterRoutes(group.Group("/feed"), feed.NewService(db))
	notificationService := notification.NewService(db)
	notification.RegisterRoutes(group, notificationService)
	dm.RegisterRoutes(group, dm.NewService(dm.NewRepo(db), dm.NewImageStoreFromEnv(s3Client), dm.UserHubPublisher{Hub: userHub}, notificationService))
	forumGroup := group.Group("/forum")
	forum.RegisterRoutes(forumGroup, forumService)
	forum_engagement.RegisterRoutes(forumGroup, forum_engagement.NewService(db))
	forumModerationService := forum_moderation.NewService(db)
	forum_moderation.RegisterLegacyRoutes(forumGroup, forumModerationService)
	forum_moderation.RegisterRoutes(forumGroup.Group("/moderation"), forumModerationService)
	debateGroup := group.Group("")
	debateGroup.Use(middleware.OptionalAuthMiddleware())
	debate.RegisterRoutes(debateGroup, debate.NewService(db, commentService))
	debate_voting.RegisterRoutes(debateGroup, debate_voting.NewService(db))
	musicGroup := group.Group("/music")
	musicGroup.Use(middleware.OptionalAuthMiddleware())
	musicService := music.NewServiceWithS3(db, s3Client)
	if userAgent := strings.TrimSpace(os.Getenv("MUSICBRAINZ_USER_AGENT")); userAgent != "" {
		musicBrainzBaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MUSICBRAINZ_BASE_URL")), "/")
		if musicBrainzBaseURL == "" {
			musicBrainzBaseURL = "https://musicbrainz.org"
		}
		musicService.WithAlbumLinkSuggestionProvider(music.NewExternalAlbumMetadataEnricher(
			&http.Client{Timeout: 5 * time.Second},
			musicBrainzBaseURL,
			"",
			"",
			userAgent,
		))
	}
	music.RegisterRoutes(musicGroup, musicService)
	portal.RegisterRoutes(group.Group("/portal"), portal.NewService(db))
	studio.RegisterRoutes(group.Group("/studio"), studio.NewService(db))
	lifecycle.RegisterRoutes(group.Group("/content"), lifecycle.NewService(db))
	timeline.RegisterRoutes(r, db)
	video.RegisterRoutes(r, db, s3Client)
	podcast.RegisterRoutes(r, db, s3Client)

	handlers.SetupAuthRoutes(r, db, emailService)
	handlers.SetupSiteRoutes(r, db)
	handlers.SetupOnboardingRoutes(r, db)
	handlers.SetupUserRoutes(r, db)
	handlers.SetupBlogUploadRoutes(r, db, s3Client)
	handlers.SetupUploadRoutes(r, db, s3Client)
	handlers.SetupSongRoutes(r, db, s3Client)
	handlers.SetupAlbumRoutes(r, db, s3Client)
	handlers.SetupArtistRoutes(r, db)
	handlers.SetupArtistWikiRoutes(r, db, s3Client)
	handlers.SetupEntryStatusRoutes(r, db)
	handlers.SetupRevisionRoutes(r, db, s3Client)
	handlers.SetupProtectionRoutes(r, db)
	handlers.SetupAdminRoutes(r, db, s3Client)

	r.GET("/ws/user", func(c *gin.Context) {
		userHub.ServeWS(c, db)
	})

	collabGroup := r.Group("/api/v1/collab")
	collabGroup.Use(middleware.AuthMiddleware())
	collabGroup.GET("/ws/:roomID", handlers.RequireBlogPostEditAccess(db, collabHub.ServeWS))
}
