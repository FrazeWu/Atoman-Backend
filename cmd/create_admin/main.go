package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"

	"atoman/internal/service"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	// Load env: prefer .env, fallback to .env.dev
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
		log.Fatalf("Unsupported DATABASE_TYPE: %s (expected: postgres)", dbType)
	}

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("Warning: create_admin is deprecated. Use init_owner with OWNER_USERNAME / OWNER_EMAIL / OWNER_PASSWORD for stable deployment bootstrap.")

	username := prompt(scanner, "Username: ")
	email := prompt(scanner, "Email: ")
	password := prompt(scanner, "Password: ")

	if len(password) < 6 {
		log.Fatal("Password must be at least 6 characters")
	}

	user, created, err := service.NewOwnerBootstrapService(db).EnsureOwner(service.OwnerBootstrapInput{
		Username: username,
		Email:    email,
		Password: password,
	})
	if err != nil {
		log.Fatalf("Failed to ensure owner user: %v", err)
	}

	if created {
		fmt.Printf("Owner user '%s' created successfully.\n", user.Username)
		return
	}
	fmt.Printf("Owner user '%s' updated successfully.\n", user.Username)
}

func prompt(scanner *bufio.Scanner, label string) string {
	fmt.Print(label)
	scanner.Scan()
	return strings.TrimSpace(scanner.Text())
}
