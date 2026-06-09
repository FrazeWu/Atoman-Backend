package main

import (
	"log"
	"os"
	"strings"

	"atoman/internal/service"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	loadEnv()

	db := mustOpenDB()
	username := strings.TrimSpace(os.Getenv("OWNER_USERNAME"))
	email := strings.TrimSpace(os.Getenv("OWNER_EMAIL"))
	password := os.Getenv("OWNER_PASSWORD")

	if username == "" || email == "" || password == "" {
		log.Fatal("OWNER_USERNAME, OWNER_EMAIL, and OWNER_PASSWORD must be set")
	}
	if len(password) < 6 {
		log.Fatal("OWNER_PASSWORD must be at least 6 characters")
	}

	user, created, err := service.NewOwnerBootstrapService(db).EnsureOwner(service.OwnerBootstrapInput{
		Username: username,
		Email:    email,
		Password: password,
	})
	if err != nil {
		log.Fatalf("ensure owner user: %v", err)
	}
	if created {
		log.Printf("owner user %q created", user.Username)
		return
	}
	log.Printf("owner user %q updated", user.Username)
}

func loadEnv() {
	if err := godotenv.Load(".env"); err == nil {
		return
	}
	_ = godotenv.Load(".env.dev")
}

func mustOpenDB() *gorm.DB {
	dbType := strings.TrimSpace(os.Getenv("DATABASE_TYPE"))
	dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dbType == "" || dbURL == "" {
		log.Fatal("DATABASE_TYPE and DATABASE_URL must be set")
	}

	var dialector gorm.Dialector
	switch dbType {
	case "postgres", "postgresql":
		dialector = postgres.Open(dbURL)
	default:
		log.Fatalf("unsupported DATABASE_TYPE: %s", dbType)
	}

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	return db
}
