package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	docs "atoman/docs"

	"github.com/google/uuid"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq" // PostgreSQL array type support
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"gorm.io/gorm"

	"atoman/internal/app"
	"atoman/internal/collab"
	"atoman/internal/config"
	"atoman/internal/middleware"
	"atoman/internal/migrationrunner"
	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/modules/lifecycle"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
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
// @name atoman_session
// @description 使用登录后写入的 atoman_session HttpOnly Cookie；写请求还需 X-CSRF-Token

func runUnifiedCommentStartupMigrations(db *gorm.DB, models ...any) error {
	if err := migrations.RunAuthPasswordResetMigration(db); err != nil {
		return fmt.Errorf("migrate password reset auth schema: %w", err)
	}
	if err := migrations.RunAuthSecurityMigration(db); err != nil {
		return fmt.Errorf("migrate auth security schema: %w", err)
	}
	if err := migrations.RunAuthOAuthMigration(db); err != nil {
		return fmt.Errorf("migrate oauth auth schema: %w", err)
	}
	if err := migrations.RunDebateWikiMigration(db); err != nil {
		return fmt.Errorf("migrate debate wiki schema: %w", err)
	}
	models = append(models,
		&model.ForumGroup{},
		&model.ForumGroupMember{},
		&model.ForumCategoryPermission{},
		&model.ForumUserModerationAction{},
		&model.ForumUserTrust{},
		&model.DiscussionTarget{},
		&model.CommentEntry{},
		&model.CommentMention{},
		&model.CommentAttachment{},
		&model.CommentLike{},
		&model.CommentReport{},
		&model.CommentTimeAnchor{},
		&model.CommentPublishRecord{},
		&model.TimelineRevisionProposal{},
		&model.DebateConclusionEvent{},
		&model.DebateRevisionReference{},
		&model.DebateVote{},
		&model.DebateRelation{},
	)
	if err := db.AutoMigrate(models...); err != nil {
		return fmt.Errorf("auto migrate startup models: %w", err)
	}
	if err := migrations.RunUnifiedCommentIndexes(db); err != nil {
		return fmt.Errorf("create unified comment indexes: %w", err)
	}
	if err := migrations.MigrateLegacyForumReplies(db); err != nil {
		return fmt.Errorf("migrate legacy forum replies: %w", err)
	}
	if err := migrations.RunMusicLyricsMigration(db); err != nil {
		return fmt.Errorf("migrate music lyrics: %w", err)
	}
	return nil
}

func runMusicBookmarkStartupMigration(db *gorm.DB) error {
	return migrations.RunMusicBookmarksPlaylistsMigration(db)
}

func runStartupDMV2Migration(db *gorm.DB) error {
	if err := migrations.RunNotificationDMIndexes(db); err != nil {
		return fmt.Errorf("notification/dm index migration: %w", err)
	}
	if err := migrations.RunDMV2Migration(db); err != nil {
		return fmt.Errorf("dm v2 migration: %w", err)
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
	if err := db.Preload("Collection").Find(&posts).Error; err != nil {
		log.Printf("WARN: failed to load posts for channel backfill: %v", err)
		return
	}

	for _, post := range posts {
		if post.ChannelID != nil {
			continue
		}
		if post.Collection == nil {
			continue
		}
		channelID := post.Collection.ChannelID
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

var internalRSSBackfillPattern = regexp.MustCompile(`(?:^|/)api(?:/v1)?/feed/rss/([^/?#]+)$`)

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
	if err := tx.Exec(`
		DELETE FROM subscriptions AS legacy_sub
		WHERE legacy_sub.feed_source_id = ?
		  AND legacy_sub.deleted_at IS NULL
		  AND EXISTS (
			SELECT 1
			FROM subscriptions AS canonical_sub
			WHERE canonical_sub.user_id = legacy_sub.user_id
			  AND canonical_sub.feed_source_id = ?
			  AND canonical_sub.deleted_at IS NULL
		  )
	`, legacy.ID, canonical.ID).Error; err != nil {
		return err
	}

	if err := tx.Model(&model.Subscription{}).
		Where("feed_source_id = ?", legacy.ID).
		Update("feed_source_id", canonical.ID).Error; err != nil {
		return err
	}

	return tx.Delete(&model.FeedSource{}, "id = ?", legacy.ID).Error
}

func missingOwnerEnvVars(username string, email string, password string) []string {
	missing := make([]string, 0, 3)
	if strings.TrimSpace(username) == "" {
		missing = append(missing, "OWNER_USERNAME")
	}
	if strings.TrimSpace(email) == "" {
		missing = append(missing, "OWNER_EMAIL")
	}
	if strings.TrimSpace(password) == "" {
		missing = append(missing, "OWNER_PASSWORD")
	}
	if len(missing) == 0 {
		return nil
	}
	return missing
}

func bootstrapOwnerFromEnv(db *gorm.DB) error {
	username := strings.TrimSpace(os.Getenv("OWNER_USERNAME"))
	email := strings.TrimSpace(os.Getenv("OWNER_EMAIL"))
	password := os.Getenv("OWNER_PASSWORD")
	missing := missingOwnerEnvVars(username, email, password)
	if len(missing) == 3 {
		log.Println("Owner bootstrap disabled: OWNER_* variables are empty")
		return nil
	}
	if len(missing) > 0 {
		log.Printf("WARN: owner bootstrap partially configured; missing %s", strings.Join(missing, ", "))
		return nil
	}

	var existing model.User
	if err := db.Where("username = ? OR email = ?", username, email).First(&existing).Error; err == nil {
		log.Printf("owner user %q already exists; startup bootstrap left it unchanged", existing.Username)
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	user, created, err := service.NewOwnerBootstrapService(db).EnsureOwner(service.OwnerBootstrapInput{
		Username: username,
		Email:    email,
		Password: password,
	})
	if err != nil {
		return err
	}
	if created {
		log.Printf("owner user %q bootstrapped successfully", user.Username)
	}
	return nil
}

func backfillInternalRSSFeedSources(db *gorm.DB) {
	var sources []model.FeedSource
	if err := db.Where("source_type = ? AND (rss_url LIKE ? OR rss_url LIKE ?)", "external_rss", "/api/feed/rss/%", "/api/v1/feed/rss/%").Find(&sources).Error; err != nil {
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

func resolveEnvFile(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "dev":
		return ".env.dev"
	case "prod":
		return ".env.prod"
	default:
		return ".env.dev"
	}
}

func loadEnvironment(mode string) string {
	envFile := resolveEnvFile(mode)
	if err := godotenv.Load(envFile); err == nil {
		return "Loaded " + envFile
	}
	if err := godotenv.Load(".env"); err == nil {
		return "Loaded .env"
	}
	return "No .env file found, using system environment variables"
}

func initializeStorageClient() *s3.S3 {
	if os.Getenv("STORAGE_TYPE") == "local" {
		log.Println("Storage mode: local (S3 disabled)")
		return nil
	}

	s3Client, err := storage.InitS3Client()
	if err != nil {
		log.Printf("WARN: S3 storage unavailable; storage-backed endpoints will return 503: %v", err)
		return nil
	}
	if err := storage.ValidateS3Connection(s3Client); err != nil {
		log.Printf("WARN: S3 storage unavailable; storage-backed endpoints will return 503: %v", err)
		return nil
	}

	log.Println("S3 storage initialized")
	return s3Client
}

func configuredAllowedOrigins() []string {
	allowedOrigins := []string{
		"http://localhost:5173",
		"http://localhost:3000",
		"http://127.0.0.1:5173",
		"http://127.0.0.1:3000",
	}
	for _, origin := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		origin = strings.TrimSpace(origin)
		if origin != "" {
			allowedOrigins = append(allowedOrigins, origin)
		}
	}
	return allowedOrigins
}

func corsMiddleware(allowedOrigins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		isAllowed := originAllowed(origin, allowedOrigins)

		if isAllowed {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, X-Request-ID")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

func originAllowed(origin string, allowedOrigins []string) bool {
	for _, allowed := range allowedOrigins {
		if origin == allowed {
			return true
		}
		if strings.HasPrefix(allowed, "*.") {
			suffix := strings.TrimPrefix(allowed, "*.")
			if strings.HasPrefix(origin, "https://") && strings.HasSuffix(strings.TrimPrefix(origin, "https://"), suffix) {
				return true
			}
			if strings.HasPrefix(origin, "http://") && strings.HasSuffix(strings.TrimPrefix(origin, "http://"), suffix) {
				return true
			}
		}
	}
	return false
}

func validateAuthEnvironment() error {
	if os.Getenv("ENV") == "production" && strings.TrimSpace(os.Getenv("AUTH_CODE_SECRET")) == "" {
		return fmt.Errorf("AUTH_CODE_SECRET environment variable is required")
	}
	return nil
}

func main() {
	mode := flag.String("mode", "dev", "startup mode: dev or prod")
	flag.Parse()

	envMessage := loadEnvironment(*mode)

	logs, err := setupLogging(loggingConfig{})
	if err != nil {
		log.Fatalf("Failed to initialize logging: %v", err)
	}
	defer func() {
		if err := logs.Close(); err != nil {
			log.Printf("WARN: failed to close log files: %v", err)
		}
	}()
	fatalLogger := logs.FatalLogger

	log.Println("Starting Atoman Backend Server...")
	log.Println(envMessage)

	if os.Getenv("GIN_MODE") == "release" {
		gin.SetMode(gin.ReleaseMode)
		log.Println("Running in production mode")
	} else {
		log.Println("Running in development mode")
	}

	if err := validateAuthEnvironment(); err != nil {
		fatalLogger.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		fatalLogger.Fatal(err)
	}
	log.Printf("Connecting to %s", databaseLogTarget(cfg.DB.Type, cfg.DB.URL))

	db, err := app.OpenDB(cfg.DB)
	if err != nil {
		fatalLogger.Fatal("Failed to connect to database: ", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		fatalLogger.Fatal("Failed to access database connection pool: ", err)
	}
	defer sqlDB.Close()
	log.Println("Database connected successfully")

	if shouldRunMigrationsOnStart(cfg.Env, os.Getenv("RUN_MIGRATIONS_ON_START")) {
		log.Println("Running database migrations...")
		if err := migrationrunner.Run(db); err != nil {
			fatalLogger.Fatal("Failed to run migrations: ", err)
		}
		log.Println("Database migrations completed")

		// Seed default site settings (idempotent)
		db.Exec(`INSERT INTO site_settings (key, value, description, updated_at)
VALUES ('forum.solved_auto_threshold', '10', '回复点赞数达到该值时自动标记为解决方案', NOW())
ON CONFLICT (key) DO NOTHING`)
		db.Exec(`INSERT INTO site_settings (key, value, description, updated_at)
VALUES ('site.module_access', '{"modules":{"feed":{"visible":true,"features":{"subscription.manage":true}},"music":{"visible":true,"features":{"music.submit":true,"music.review":true}},"blog":{"visible":true,"features":{"post.create":true,"channel.manage":true}},"forum":{"visible":true,"features":{"topic.create":true,"category.request":true}},"debate":{"visible":true,"features":{"debate.create":true,"debate.edit":true}},"timeline":{"visible":true,"features":{"timeline.edit":true}},"podcast":{"visible":true,"features":{"podcast.publish":true}},"video":{"visible":true,"features":{"video.publish":true}}}}', '模块可见性与功能开放配置', NOW())
ON CONFLICT (key) DO NOTHING`)

		log.Println("Running blog channel field backfill...")
		backfillBlogChannelFields(db)
		log.Println("Running external RSS full text enablement backfill...")
		backfillExternalRSSFullTextEnabled(db)
		log.Println("Running internal RSS feed source backfill...")
		backfillInternalRSSFeedSources(db)

		ensureSoftDeleteColumns(db)

		if err := bootstrapOwnerFromEnv(db); err != nil {
			log.Fatal("Failed to bootstrap owner user: ", err)
		}

	} else {
		log.Println("Skipping startup migrations; run cmd/migrate before starting the service")
	}

	// Initialize email service (without Redis)
	emailService := service.NewEmailServiceWithoutRedis(db)
	log.Println("Email service initialized (Redis disabled)")

	s3Client := initializeStorageClient()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rssCronDone := service.StartRSSCron(ctx, db)
	fullTextWorkerDone := service.StartFullTextWorker(ctx, db)
	lifecycleWorkerDone := lifecycle.StartWorker(ctx, db)

	log.Println("Initializing Casbin Enforcer...")
	if err := middleware.InitCasbin(db); err != nil {
		fatalLogger.Fatal("Failed to initialize Casbin: ", err)
	}

	r := gin.New()
	if err := r.SetTrustedProxies([]string{"127.0.0.1", "::1"}); err != nil {
		fatalLogger.Fatal("Failed to configure trusted proxies: ", err)
	}
	r.Use(middleware.RequestIDMiddleware())
	r.Use(middleware.AccessLogMiddleware(log.New(gin.DefaultWriter, "", log.Flags())))
	r.Use(gin.Recovery())
	docs.SwaggerInfo.BasePath = "/api/v1"

	r.Use(corsMiddleware(configuredAllowedOrigins()))
	registerHealthRoutes(r, db)

	// Add global Optional Auth and Casbin Middleware
	r.Use(middleware.OptionalAuthMiddleware())
	r.Use(middleware.CasbinMiddleware())

	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	if os.Getenv("STORAGE_TYPE") == "local" {
		r.Static("/uploads", "./uploads")
		log.Println("Static files served from ./uploads directory")
	}

	userHub := collab.NewUserHub()
	collabHub := collab.NewHub()
	app.RegisterV1Routes(r, db, emailService, s3Client, userHub, collabHub)

	// 404 handler - must be last
	r.NoRoute(func(c *gin.Context) {
		httpx.Error(c, apperr.NotFound("system.not_found", "Not found"))
	})

	server := newHTTPServer(":"+cfg.Port, r)
	log.Printf("Server starting on port %s", cfg.Port)
	if err := serveUntilShutdown(ctx, server, shutdownTimeout); err != nil {
		fatalLogger.Fatal("Failed to start server: ", err)
	}
	if err := waitForWorkers(shutdownTimeout, rssCronDone, fullTextWorkerDone, lifecycleWorkerDone); err != nil {
		log.Printf("WARN: timed out waiting for background workers to stop: %v", err)
	}
	log.Println("Server stopped")
}

func waitForWorkers(timeout time.Duration, workers ...<-chan struct{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for _, done := range workers {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func databaseLogTarget(dbType string, rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if strings.Contains(rawURL, "=") && !strings.Contains(rawURL, "://") {
		return databaseLogTargetFromDSN(dbType, rawURL)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return strings.TrimSpace(dbType) + " database"
	}

	parts := []string{strings.TrimSpace(dbType) + " database"}
	if host := parsed.Host; host != "" {
		parts = append(parts, "host="+host)
	}
	if dbName := strings.TrimPrefix(parsed.EscapedPath(), "/"); dbName != "" {
		if decoded, err := url.PathUnescape(dbName); err == nil {
			dbName = decoded
		}
		parts = append(parts, "dbname="+dbName)
	}
	return strings.Join(parts, " ")
}

func databaseLogTargetFromDSN(dbType string, dsn string) string {
	values := map[string]string{}
	for _, field := range strings.Fields(dsn) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		values[key] = strings.Trim(value, "'\"")
	}

	parts := []string{strings.TrimSpace(dbType) + " database"}
	host := values["host"]
	if port := values["port"]; host != "" && port != "" {
		host += ":" + port
	}
	if host != "" {
		parts = append(parts, "host="+host)
	}
	if dbName := values["dbname"]; dbName != "" {
		parts = append(parts, "dbname="+dbName)
	}
	return strings.Join(parts, " ")
}
