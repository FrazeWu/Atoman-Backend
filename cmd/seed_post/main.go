package main

import (
	"log"
	"os"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	// Load env
	if err := godotenv.Load(".env"); err != nil {
		if err2 := godotenv.Load(".env.dev"); err2 != nil {
			log.Println("No .env file found, using system environment variables")
		} else {
			log.Println("Loaded .env.dev")
		}
	} else {
		log.Println("Loaded .env")
	}

	dbType := os.Getenv("DATABASE_TYPE")
	dbURL := os.Getenv("DATABASE_URL")
	if dbType == "" || dbURL == "" {
		log.Fatal("DATABASE_TYPE and DATABASE_URL must be set")
	}

	var dialector gorm.Dialector
	switch dbType {
	case "postgres", "postgresql":
		dialector = postgres.Open(dbURL)
	default:
		log.Fatalf("Unsupported DATABASE_TYPE: %s", dbType)
	}

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	// Find user
	var user model.User
	if err := db.Where("username = ?", "fazong").First(&user).Error; err != nil {
		if err := db.Where("username = ?", "owner").First(&user).Error; err != nil {
			log.Fatalf("Could not find user fazong or owner: %v", err)
		}
	}
	log.Printf("Found user: %s (%s)", user.Username, user.UUID)

	// Read content from content.md
	contentBytes, err := os.ReadFile("cmd/seed_post/content.md")
	if err != nil {
		// Try reading from absolute path or current dir
		contentBytes, err = os.ReadFile("content.md")
		if err != nil {
			log.Fatalf("Failed to read content.md: %v", err)
		}
	}
	content := string(contentBytes)

	post := model.Post{
		UserID:     user.UUID,
		Title:      "构建现代极简主义设计系统 (Building a Modern Minimalist Design System)",
		Content:    content,
		Summary:    "深入探讨如何构建一个现代、优雅且响应迅速的极简主义设计系统，并在本次重构中实现真正的视觉平衡。",
		Status:     "published",
		Visibility: "public",
	}

	post.ID, _ = uuid.NewV7()
	post.CreatedAt = time.Now()
	post.UpdatedAt = time.Now()

	if err := db.Create(&post).Error; err != nil {
		log.Fatalf("Failed to create seed post: %v", err)
	}

	log.Printf("Successfully created seed post: %s (ID: %s)", post.Title, post.ID)
}
