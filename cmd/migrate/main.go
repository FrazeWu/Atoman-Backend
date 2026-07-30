package main

import (
	"flag"
	"log"
	"os"

	"github.com/joho/godotenv"
	"gorm.io/gorm"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/migrationrunner"
)

func main() {
	envFile := flag.String("env", ".env.dev", "env file to load before migrating")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		log.Printf("WARN: load %s: %v", *envFile, err)
	}

	dbType := os.Getenv("DATABASE_TYPE")
	if dbType == "" {
		log.Fatal("DATABASE_TYPE is required")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	db, err := app.OpenDB(config.DBConfig{Type: dbType, URL: dbURL})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}

	if err := runMigrations(db); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	log.Println("migrations completed")
}

func runMigrations(db *gorm.DB) error {
	return migrationrunner.Run(db)
}

func migrateSchema(db *gorm.DB) error {
	return migrationrunner.MigrateSchema(db)
}
