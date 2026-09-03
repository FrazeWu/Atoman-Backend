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
	"atoman/internal/platform/authctx"
	"atoman/internal/storage"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

func main() {
	envFile := flag.String("env", ".env.dev", "environment file")
	userID := flag.String("user-id", "", "UUID of the catalog owner")
	limit := flag.Int("limit", 20, "maximum number of popular albums (max 100)")
	maxItemMB := flag.Int64("max-item-mb", 512, "maximum selected download size per album")
	apply := flag.Bool("apply", false, "download, queue processing, and write import records")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		log.Printf("WARN: load %s: %v", *envFile, err)
	}
	parsedUserID, err := uuid.Parse(strings.TrimSpace(*userID))
	if err != nil {
		log.Fatal("-user-id must be a valid UUID")
	}
	db, err := app.OpenDB(config.DBConfig{
		Type: strings.TrimSpace(os.Getenv("DATABASE_TYPE")),
		URL:  strings.TrimSpace(os.Getenv("DATABASE_URL")),
	})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	var owner model.User
	if err := db.First(&owner, "uuid = ?", parsedUserID).Error; err != nil {
		log.Fatalf("load importing user: %v", err)
	}
	if *apply && !db.Migrator().HasTable(&model.MusicExternalImport{}) {
		log.Fatal("music_external_imports is missing; run cmd/migrate first")
	}

	var service *music.Service
	if *apply {
		s3Client, err := storage.InitS3Client()
		if err != nil {
			log.Fatalf("initialize object storage: %v", err)
		}
		service = music.NewServiceWithS3(db, s3Client)
	}
	userAgent := strings.TrimSpace(os.Getenv("INTERNET_ARCHIVE_USER_AGENT"))
	if userAgent == "" {
		userAgent = "Atoman open music importer"
	}
	importer := music.NewInternetArchiveImporter(
		db,
		service,
		&http.Client{Timeout: 30 * time.Minute},
		authctx.CurrentUser{ID: owner.UUID, Username: owner.Username, Role: owner.Role},
		userAgent,
	)
	results, err := importer.ImportPopular(context.Background(), music.InternetArchiveImportOptions{
		Limit: *limit, MaxItemBytes: *maxItemMB * 1024 * 1024, Apply: *apply,
	})
	if err != nil {
		log.Fatal(err)
	}
	failed := 0
	for _, result := range results {
		if result.Err != nil {
			failed++
			log.Printf("FAILED %s: %v", result.Plan.Identifier, result.Err)
			continue
		}
		log.Printf("%s %s | %s | %s | downloads=%d tracks=%d bytes=%d session=%s",
			strings.ToUpper(result.Status), result.Plan.Identifier, result.Plan.Creator, result.Plan.LicenseCode,
			result.Plan.Downloads, countAudioFiles(result.Plan.Files), result.Plan.TotalBytes, uuidValue(result.ImportSessionID),
		)
	}
	if failed > 0 {
		log.Fatalf("%d of %d candidates failed", failed, len(results))
	}
	if !*apply {
		log.Println("dry run only; rerun with -apply after reviewing the plan")
	}
}

func countAudioFiles(files []music.InternetArchiveImportFile) int {
	count := 0
	for _, file := range files {
		if file.Kind == "audio" {
			count++
		}
	}
	return count
}

func uuidValue(value *uuid.UUID) string {
	if value == nil {
		return "-"
	}
	return value.String()
}
