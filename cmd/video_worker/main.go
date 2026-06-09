package main

import (
	"log"
	"os"
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

	worker := service.VideoPreviewWorker{
		DB: db,
		Generator: service.FFmpegPreviewGenerator{
			UploadsRoot: ".",
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
