package main

import (
	"log"
	"os"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"atoman/internal/handlers"
	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/service"
	"atoman/internal/storage"
	"context"
)

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
		&model.Notification{},
		&model.AlbumCorrection{},
		&model.SongCorrection{},
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

func main() {
	log.Println("Starting Atoman Backend Server...")

	if err := godotenv.Load(".env"); err != nil {
		if err := godotenv.Load(".env.dev"); err != nil {
			log.Println("No .env file found, using system environment variables")
		} else {
			log.Println("Loaded .env.dev")
		}
	} else {
		log.Println("Loaded .env")
	}

	if os.Getenv("GIN_MODE") == "release" {
		gin.SetMode(gin.ReleaseMode)
		log.Println("Running in production mode")
	} else {
		log.Println("Running in development mode")
	}

	dbType := os.Getenv("DATABASE_TYPE")
	if dbType == "" {
		log.Fatal("DATABASE_TYPE environment variable is required (postgres or sqlite)")
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
	case "sqlite":
		dialector = sqlite.Open(dbURL)
	default:
		log.Fatal("Unsupported DATABASE_TYPE: ", dbType, " (expected: postgres or sqlite)")
	}

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database: ", err)
	}
	log.Println("Database connected successfully")

	log.Println("Running database migrations...")
	if err := db.AutoMigrate(
		&model.User{},
		&model.UserSettings{},
		&model.Artist{},
		&model.Album{},
		&model.Song{},
		&model.SongCorrection{},
		&model.AlbumCorrection{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
		&model.Comment{},
		&model.Like{},
		&model.Bookmark{},
		&model.FeedSource{},
		&model.FeedItem{},
		&model.FeedItemRead{},
		&model.SubscriptionGroup{},
		&model.Notification{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumReply{},
		&model.ForumLike{},
		&model.Debate{},
		&model.Argument{},
		&model.DebateVote{},
		&model.VoteHistory{},
	); err != nil {
		log.Fatal("Failed to run migrations: ", err)
	}
	log.Println("Database migrations completed")

	ensureSoftDeleteColumns(db)

	// Initialize Redis client for email service
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	redisClient := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	// Test Redis connection
	if _, err := redisClient.Ping(context.Background()).Result(); err != nil {
		log.Printf("WARN: Redis connection failed: %v. Email verification will not work.", err)
	} else {
		log.Println("Redis connected successfully")
	}

	// Initialize email service
	emailService := service.NewEmailService(redisClient)
	log.Println("Email service initialized")

	var s3Client *s3.S3
	if os.Getenv("STORAGE_TYPE") == "local" {
		log.Println("Storage mode: local (S3 disabled)")
	} else {
		var err error
		s3Client, err = storage.InitS3Client()
		if err != nil {
			log.Fatal("Failed to create S3 client: ", err)
		}
		if err := storage.ValidateS3Connection(s3Client); err != nil {
			log.Fatal("Failed to validate S3 connection: ", err)
		}
		log.Println("S3 storage initialized")
	}

	log.Println("Starting background RSS cron worker...")
	service.StartRSSCron(db)

	log.Println("Initializing Casbin Enforcer...")
	if err := middleware.InitCasbin(db); err != nil {
		log.Fatal("Failed to initialize Casbin: ", err)
	}

	r := gin.Default()

	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	// Add global Optional Auth and Casbin Middleware
	r.Use(middleware.OptionalAuthMiddleware())
	r.Use(middleware.CasbinMiddleware())

	handlers.SetupAuthRoutes(r, db, emailService)
	handlers.SetupUserRoutes(r, db)
	handlers.SetupBlogChannelRoutes(r, db)
	handlers.SetupBlogPostRoutes(r, db)
	handlers.SetupBlogInteractionRoutes(r, db)
	handlers.SetupBlogUploadRoutes(r, db, s3Client)
	handlers.SetupFeedRoutes(r, db)
	handlers.SetupNotificationRoutes(r, db)
	handlers.SetupSongRoutes(r, db, s3Client)
	handlers.SetupAlbumRoutes(r, db, s3Client)
	handlers.SetupArtistRoutes(r, db)
	handlers.SetupCorrectionRoutes(r, db, s3Client)
	handlers.SetupForumRoutes(r, db)
	handlers.SetupDebateRoutes(r, db)

	// Admin routes
	admin := r.Group("/api/admin")
	admin.Use(middleware.AdminMiddleware(db))
	{
		handlers.SetupAdminRoutes(r, db, s3Client)
	}

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
