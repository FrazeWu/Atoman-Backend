package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	docs "atoman/docs"

	"github.com/google/uuid"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq" // PostgreSQL array type support
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"atoman/internal/app"
	"atoman/internal/collab"
	"atoman/internal/middleware"
	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/service"
	"atoman/internal/storage"
)

//go:generate go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/start_server/main.go -d ../.. -o ../../docs

// @title Atoman API
// @version 1.0
// @description Atoman 后端 API 文档。
// @BasePath /api/v1
// @schemes http https
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description 使用 Bearer Token，例如：Bearer <token>
// @securityDefinitions.apikey CookieAuth
// @in cookie
// @name atoman_token
// @description 使用登录后写入的 atoman_token Cookie

func prepareCommentTargetMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.Comment{}) {
		return nil
	}

	if !db.Migrator().HasColumn(&model.Comment{}, "TargetType") {
		if err := db.Exec(`ALTER TABLE comments ADD COLUMN target_type varchar(16)`).Error; err != nil {
			return err
		}
	}

	if !db.Migrator().HasColumn(&model.Comment{}, "TargetID") {
		if err := db.Exec(`ALTER TABLE comments ADD COLUMN target_id uuid`).Error; err != nil {
			return err
		}
	}

	if db.Migrator().HasColumn(&model.Comment{}, "post_id") {
		if err := db.Exec(`UPDATE comments SET target_type = 'post' WHERE target_type IS NULL AND post_id IS NOT NULL`).Error; err != nil {
			return err
		}
		if err := db.Exec(`UPDATE comments SET target_id = post_id WHERE target_id IS NULL AND post_id IS NOT NULL`).Error; err != nil {
			return err
		}
	}

	return nil
}

func ensureSoftDeleteColumns(db *gorm.DB) {
	softDeleteModels := []interface{}{
		&model.User{},
		&model.Artist{},
		&model.Album{},
		&model.Song{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
		&model.Comment{},
		&model.FeedSource{},
		&model.FeedItem{},
		&model.AlbumCorrection{},
		&model.SongCorrection{},
		&model.ArtistCorrection{},
		&model.PodcastEpisode{},
	}

	for _, m := range softDeleteModels {
		if !db.Migrator().HasTable(m) {
			continue
		}
		if !db.Migrator().HasColumn(m, "deleted_at") {
			if err := db.Migrator().AddColumn(m, "DeletedAt"); err != nil {
				log.Printf("WARN: failed to add deleted_at for %T: %v", m, err)
			}
		}
	}
}

func backfillBlogChannelFields(db *gorm.DB) {
	var channels []model.Channel
	if err := db.Find(&channels).Error; err != nil {
		log.Printf("WARN: failed to load channels for backfill: %v", err)
		return
	}

	for _, channel := range channels {
		updates := map[string]interface{}{}
		if strings.TrimSpace(channel.Slug) == "" {
			base := strings.TrimSpace(channel.Name)
			if base == "" {
				base = "channel"
			}
			candidate := handlersSlugify(base)
			for {
				var count int64
				query := db.Model(&model.Channel{}).Where("slug = ?", candidate).Where("id <> ?", channel.ID)
				if err := query.Count(&count).Error; err != nil {
					log.Printf("WARN: failed to check slug uniqueness for channel %s: %v", channel.ID, err)
					break
				}
				if count == 0 {
					updates["slug"] = candidate
					break
				}
				candidate = candidate + "-" + uuid.NewString()[:8]
			}
		}
		if len(updates) > 0 {
			if err := db.Model(&model.Channel{}).Where("id = ?", channel.ID).Updates(updates).Error; err != nil {
				log.Printf("WARN: failed to backfill channel %s: %v", channel.ID, err)
			}
		}
	}

	var posts []model.Post
	if err := db.Preload("Collections").Find(&posts).Error; err != nil {
		log.Printf("WARN: failed to load posts for channel backfill: %v", err)
		return
	}

	for _, post := range posts {
		if post.ChannelID != nil {
			continue
		}
		if len(post.Collections) == 0 {
			continue
		}
		channelID := post.Collections[0].ChannelID
		if err := db.Model(&model.Post{}).Where("id = ?", post.ID).Update("channel_id", channelID).Error; err != nil {
			log.Printf("WARN: failed to backfill post %s channel_id: %v", post.ID, err)
		}
	}
}

func backfillExternalRSSFullTextEnabled(db *gorm.DB) {
	if err := db.Model(&model.FeedSource{}).
		Where("source_type = ? AND full_text_enabled = ?", "external_rss", false).
		Update("full_text_enabled", true).Error; err != nil {
		log.Printf("WARN: failed to backfill external RSS full_text_enabled: %v", err)
	}
}

var internalRSSBackfillPattern = regexp.MustCompile(`(?:^|/)api/feed/rss/([^/?#]+)$`)

func resolveInternalRSSUserIDForBackfill(db *gorm.DB, rawURL string) (uuid.UUID, error) {
	m := internalRSSBackfillPattern.FindStringSubmatch(strings.TrimSpace(rawURL))
	if len(m) < 2 {
		return uuid.UUID{}, fmt.Errorf("not an internal RSS URL")
	}

	var user model.User
	if err := db.Where("username = ?", m[1]).First(&user).Error; err != nil {
		return uuid.UUID{}, err
	}
	return user.UUID, nil
}

func buildInternalFeedSourceHash(targetType string, targetID uuid.UUID) string {
	raw := fmt.Sprintf("%s:%s", targetType, targetID.String())
	h := sha256.New()
	h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))
}

func mergeInternalRSSFeedSourceIntoCanonical(tx *gorm.DB, legacy model.FeedSource, canonical model.FeedSource) error {
	if err := tx.Model(&model.Subscription{}).
		Where("feed_source_id = ?", legacy.ID).
		Update("feed_source_id", canonical.ID).Error; err != nil {
		return err
	}

	return tx.Delete(&model.FeedSource{}, "id = ?", legacy.ID).Error
}

func backfillInternalRSSFeedSources(db *gorm.DB) {
	var sources []model.FeedSource
	if err := db.Where("source_type = ? AND rss_url LIKE ?", "external_rss", "/api/feed/rss/%").Find(&sources).Error; err != nil {
		log.Printf("WARN: failed to load internal RSS feed source backfill candidates: %v", err)
		return
	}

	for _, source := range sources {
		userID, err := resolveInternalRSSUserIDForBackfill(db, source.RssURL)
		if err != nil {
			log.Printf("WARN: failed to resolve internal RSS feed source %s (%s): %v", source.ID, source.RssURL, err)
			continue
		}

		targetHash := buildInternalFeedSourceHash("internal_user", userID)
		var canonical model.FeedSource
		if err := db.Where("hash = ?", targetHash).First(&canonical).Error; err == nil {
			if canonical.ID == source.ID {
				continue
			}
			if err := db.Transaction(func(tx *gorm.DB) error {
				return mergeInternalRSSFeedSourceIntoCanonical(tx, source, canonical)
			}); err != nil {
				log.Printf("WARN: failed to merge internal RSS feed source %s into canonical %s: %v", source.ID, canonical.ID, err)
			}
			continue
		}

		updates := map[string]interface{}{
			"source_type":   "internal_user",
			"source_id":     userID,
			"rss_url":       "",
			"provider":      "internal",
			"canonical_url": "",
			"site_url":      "",
			"hash":          targetHash,
		}
		if err := db.Model(&model.FeedSource{}).Where("id = ?", source.ID).Updates(updates).Error; err != nil {
			log.Printf("WARN: failed to backfill internal RSS feed source %s: %v", source.ID, err)
		}
	}
}

func handlersSlugify(value string) string {
	slug := strings.ToLower(strings.TrimSpace(value))
	replacer := func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r >= '一' && r <= '龥':
			return r
		default:
			return '-'
		}
	}
	mapped := strings.Map(replacer, slug)
	mapped = strings.Trim(mapped, "-")
	mapped = strings.Join(strings.FieldsFunc(mapped, func(r rune) bool { return r == '-' }), "-")
	if mapped == "" {
		return "channel"
	}
	return mapped
}

func main() {
	log.Println("Starting Atoman Backend Server...")

	if err := godotenv.Load(".env.dev"); err == nil {
		log.Println("Loaded .env.dev")
	} else if err := godotenv.Load(".env"); err == nil {
		log.Println("Loaded .env")
	} else {
		log.Println("No .env file found, using system environment variables")
	}

	if os.Getenv("GIN_MODE") == "release" {
		gin.SetMode(gin.ReleaseMode)
		log.Println("Running in production mode")
	} else {
		log.Println("Running in development mode")
	}

	if os.Getenv("JWT_SECRET") == "" {
		log.Fatal("JWT_SECRET environment variable is required")
	}

	dbType := os.Getenv("DATABASE_TYPE")
	if dbType == "" {
		log.Fatal("DATABASE_TYPE environment variable is required (postgres)")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	log.Printf("Connecting to %s database: %s", dbType, dbURL)

	var dialector gorm.Dialector
	switch dbType {
	case "postgres", "postgresql":
		dialector = postgres.Open(dbURL)
	default:
		log.Fatal("Unsupported DATABASE_TYPE: ", dbType, " (expected: postgres)")
	}

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database: ", err)
	}
	log.Println("Database connected successfully")

	// Always run migrations on startup (AutoMigrate is idempotent)
	{
		log.Println("Running database migrations...")

		// Enable required PostgreSQL extensions
		if err := db.Exec("CREATE EXTENSION IF NOT EXISTS ltree").Error; err != nil {
			log.Printf("WARN: failed to enable ltree extension: %v", err)
		}
		if err := prepareCommentTargetMigration(db); err != nil {
			log.Fatal("Failed to prepare comment target migration: ", err)
		}
		if err := migrations.RunBlogGuestCommentsMigration(db); err != nil {
			log.Fatal("Failed to run blog guest comments migration: ", err)
		}
		if err := db.AutoMigrate(
			&model.User{},
			&model.UserSettings{},
			&model.Artist{},
			&model.Album{},
			&model.Song{},
			&model.SongCorrection{},
			&model.AlbumCorrection{},
			&model.ArtistCorrection{},
			&model.Channel{},
			&model.Collection{},
			&model.Post{},
			&model.BlogPostRating{},
			&model.BlogDraft{},
			&model.MediaAsset{},
			&model.Comment{},
			&model.Like{},
			&model.Bookmark{},
			&model.BookmarkFolder{},
			// Phase 1 compatibility models for /api/v1 feed/subscription/notification modules.
			&model.FeedSource{},
			&model.Subscription{},
			&model.Follow{},
			&model.FeedItem{},
			&model.FeedItemRead{},
			&model.FeedStarGroup{},
			&model.FeedItemStar{},
			&model.ReadingListItem{},
			&model.SubscriptionGroup{},
			&model.ForumCategory{},
			&model.ForumTopic{},
			&model.ForumReply{},
			&model.ForumLike{},
			&model.ForumBookmark{},
			&model.ForumDraft{},
			&model.Notification{},
			&model.DMConversation{},
			&model.DMMessage{},
			&model.ActivityLog{},
			&model.AuditLog{},
			&model.Debate{},
			&model.Argument{},
			&model.DebateVote{},
			&model.VoteHistory{},
			&model.DebateConcludeVote{},
			&model.EmailVerificationCode{},
			&model.TimelineEvent{},
			&model.TimelinePerson{},
			&model.PersonLocation{},
			&model.TimelineRevision{},
			// Revision / wiki system
			&model.Revision{},
			&model.EditConflict{},
			&model.ContentProtection{},
			&model.Discussion{},
			// Music wiki extensions
			&model.ArtistAlias{},
			&model.ArtistMerge{},
			&model.LyricAnnotation{},
			&model.MusicEdit{},
			&model.MusicEditVote{},
			&model.MusicEditDecision{},
			&model.MusicEditChange{},
			// Podcast
			&model.PodcastEpisode{},
			// Video module
			&model.Video{},
			&model.VideoProcessingJob{},
			&model.VideoTag{},
			&model.VideoCollection{},
			&model.VideoTagRelation{},
			// Forum extensions
			&model.ForumReport{},
			&model.CategoryRequest{},
			&model.ForumModeratorAssignment{},
			&model.SiteSetting{},
		); err != nil {
			log.Fatal("Failed to run migrations: ", err)
		}
		log.Println("Database migrations completed")

		if err := migrations.RunNotificationDMIndexes(db); err != nil {
			log.Printf("WARN: notification/dm index migration failed: %v", err)
		}

		// Seed default site settings (idempotent)
		db.Exec(`INSERT INTO site_settings (key, value, description, updated_at)
VALUES ('forum.solved_auto_threshold', '10', '回复点赞数达到该值时自动标记为解决方案', NOW())
ON CONFLICT (key) DO NOTHING`)
		db.Exec(`INSERT INTO site_settings (key, value, description, updated_at)
VALUES ('site.module_access', '{"modules":{"feed":{"visible":true,"features":{"subscription.manage":true}},"music":{"visible":true,"features":{"music.submit":true,"music.review":true}},"blog":{"visible":true,"features":{"post.create":true,"channel.manage":true}},"forum":{"visible":true,"features":{"topic.create":true,"category.request":true}},"debate":{"visible":true,"features":{"debate.create":true,"argument.create":true}},"timeline":{"visible":true,"features":{"timeline.edit":true}},"podcast":{"visible":true,"features":{"podcast.publish":true}},"video":{"visible":true,"features":{"video.publish":true}}}}', '模块可见性与功能开放配置', NOW())
ON CONFLICT (key) DO NOTHING`)

		log.Println("Running blog channel field backfill...")
		backfillBlogChannelFields(db)
		log.Println("Running external RSS full text enablement backfill...")
		backfillExternalRSSFullTextEnabled(db)

		ensureSoftDeleteColumns(db)

		// Run forum-specific migrations (ltree extension, new columns, backfill)
		if err := service.RunForumMigrations(db); err != nil {
			log.Printf("WARN: forum migrations had errors: %v", err)
		}
	} // end migrations block

	// Initialize email service (without Redis)
	emailService := service.NewEmailServiceWithoutRedis(db)
	log.Println("Email service initialized (Redis disabled)")

	var s3Client *s3.S3
	if os.Getenv("STORAGE_TYPE") == "local" {
		log.Println("Storage mode: local (S3 disabled)")
	} else {
		var err error
		s3Client, err = storage.InitS3Client()
		if err != nil {
			log.Println("WARN: Failed to create S3 client:", err)
			log.Println("S3 storage disabled, falling back to local storage")
			s3Client = nil
		} else if err := storage.ValidateS3Connection(s3Client); err != nil {
			log.Println("WARN: Failed to validate S3 connection:", err)
			log.Println("S3 storage disabled, falling back to local storage")
			s3Client = nil
		} else {
			log.Println("S3 storage initialized")
		}
	}

	log.Println("Starting background RSS cron worker...")
	service.StartRSSCron(db)
	log.Println("Starting background full text worker...")
	service.StartFullTextWorker(db)

	log.Println("Initializing Casbin Enforcer...")
	if err := middleware.InitCasbin(db); err != nil {
		log.Fatal("Failed to initialize Casbin: ", err)
	}

	r := gin.Default()
	docs.SwaggerInfo.BasePath = "/api/v1"

	// Configure allowed origins based on environment
	allowedOrigins := []string{
		"http://localhost:5173",
		"http://localhost:3000",
		"http://127.0.0.1:5173",
		"http://127.0.0.1:3000",
	}
	if env := os.Getenv("ENV"); env == "production" {
		// Add production domains from environment variable
		if prodOrigins := os.Getenv("ALLOWED_ORIGINS"); prodOrigins != "" {
			allowedOrigins = append(allowedOrigins, strings.Split(prodOrigins, ",")...)
		}
	}

	r.Use(func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		isAllowed := false
		for _, allowed := range allowedOrigins {
			if origin == allowed {
				isAllowed = true
				break
			}
		}

		if isAllowed {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
		} else {
			// For development, allow all origins (but log a warning)
			if os.Getenv("ENV") != "production" {
				c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			}
		}

		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, X-Request-ID")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	// Add global Optional Auth and Casbin Middleware
	r.Use(middleware.OptionalAuthMiddleware())
	r.Use(middleware.CasbinMiddleware())

	// Serve static files from uploads directory
	r.Static("/uploads", "./uploads")
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	log.Println("Static files served from ./uploads directory")

	userHub := collab.NewUserHub()
	collabHub := collab.NewHub()
	app.RegisterV1Routes(r, db, emailService, s3Client, userHub, collabHub)

	// 404 handler - must be last
	r.NoRoute(func(c *gin.Context) {
		c.JSON(404, gin.H{"error": "Not found"})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Server starting on port %s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatal("Failed to start server: ", err)
	}
}
