package main

import (
	"errors"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq" // PostgreSQL array type support
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"atoman/internal/service"
)

func main() {
	log.Println("Starting Atoman video preview worker...")

	if err := godotenv.Load(".env.dev"); err == nil {
		log.Println("Loaded .env.dev")
	} else if err := godotenv.Load(".env"); err == nil {
		log.Println("Loaded .env")
	} else {
		log.Println("No .env file found, using system environment variables")
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

	log.Printf("Connecting to %s", databaseLogTarget(dbType, dbURL))

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

	uploadsRoot, err := uploadsRootFromEnv()
	if err != nil {
		log.Fatal("Invalid UPLOADS_ROOT: ", err)
	}

	worker := service.VideoPreviewWorker{
		DB: db,
		Generator: service.FFmpegPreviewGenerator{
			UploadsRoot: uploadsRoot,
		},
		MaxAttempts: 3,
	}

	for {
		processed, err := worker.ProcessNext()
		if err != nil {
			log.Printf("video preview worker error: %v", err)
		}
		if !processed {
			time.Sleep(5 * time.Second)
		}
	}
}

func uploadsRootFromEnv() (string, error) {
	root := strings.TrimSpace(os.Getenv("UPLOADS_ROOT"))
	if root == "" {
		return "", errors.New("UPLOADS_ROOT is required for video_worker")
	}

	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", os.ErrInvalid
	}
	return root, nil
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
