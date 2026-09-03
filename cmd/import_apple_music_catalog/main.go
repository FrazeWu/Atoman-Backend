package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/model"
	"atoman/internal/modules/music"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

func main() {
	envFile := flag.String("env", ".env.dev", "environment file")
	userID := flag.String("user-id", "", "UUID of the catalog owner")
	storefront := flag.String("storefront", "CN", "Apple storefront country code")
	artistLimit := flag.Int("artist-limit", 100, "maximum derived chart artists (max 100)")
	albumLimit := flag.Int("album-limit", 20, "maximum albums per artist (max 200)")
	songLimit := flag.Int("song-limit", 200, "maximum songs returned per artist (max 200)")
	requestDelay := flag.Duration("request-delay", 3100*time.Millisecond, "delay between Apple API requests")
	apply := flag.Bool("apply", false, "write metadata to the catalog")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		log.Printf("WARN: load %s: %v", *envFile, err)
	}
	var db *gorm.DB
	var ownerID uuid.UUID
	var err error
	if *apply {
		db, err = app.OpenDB(config.DBConfig{Type: strings.TrimSpace(os.Getenv("DATABASE_TYPE")), URL: strings.TrimSpace(os.Getenv("DATABASE_URL"))})
		if err != nil {
			log.Fatalf("open database: %v", err)
		}
		var owner model.User
		ownerQuery := db
		if value := strings.TrimSpace(*userID); value != "" {
			parsedOwnerID, err := uuid.Parse(value)
			if err != nil {
				log.Fatal("-user-id must be a valid UUID")
			}
			ownerQuery = ownerQuery.Where("uuid = ?", parsedOwnerID)
		} else {
			ownerUsername := strings.TrimSpace(os.Getenv("OWNER_USERNAME"))
			if ownerUsername == "" {
				log.Fatal("-user-id or OWNER_USERNAME is required when -apply is set")
			}
			ownerQuery = ownerQuery.Where("username = ?", ownerUsername)
		}
		if err := ownerQuery.First(&owner).Error; err != nil {
			log.Fatalf("load catalog owner: %v", err)
		}
		ownerID = owner.UUID
		if !db.Migrator().HasTable(&model.MusicCatalogLink{}) {
			log.Fatal("music_catalog_links is missing; run cmd/migrate first")
		}
	}

	userAgent := strings.TrimSpace(os.Getenv("APPLE_MUSIC_USER_AGENT"))
	if userAgent == "" {
		userAgent = "Atoman Apple catalog metadata importer"
	}
	importer := music.NewAppleCatalogImporter(db, &http.Client{Timeout: 30 * time.Second}, ownerID, userAgent)
	summary, err := importer.Import(context.Background(), music.AppleCatalogImportOptions{
		Storefront: *storefront, ArtistLimit: *artistLimit, AlbumLimit: *albumLimit,
		SongLimit: *songLimit, RequestDelay: *requestDelay, Apply: *apply,
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("storefront=%s candidates=%d artists=%d albums=%d songs=%d applied=%t", summary.Storefront, summary.Candidates, summary.Artists, summary.Albums, summary.Songs, summary.Applied)
	if !*apply {
		log.Print("dry run only; rerun with -apply after reviewing the totals")
	}
}
