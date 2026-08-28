package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/joho/godotenv"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/modules/books"
)

func main() {
	envFile := flag.String("env", ".env.dev", "env file to load before importing")
	query := flag.String("query", "", "Open Library search query")
	limit := flag.Int("limit", 20, "maximum number of records to import")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		log.Printf("WARN: load %s: %v", *envFile, err)
	}
	if strings.TrimSpace(*query) == "" {
		log.Fatal("-query is required")
	}

	dbType := strings.TrimSpace(os.Getenv("DATABASE_TYPE"))
	dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dbType == "" || dbURL == "" {
		log.Fatal("DATABASE_TYPE and DATABASE_URL are required")
	}
	db, err := app.OpenDB(config.DBConfig{Type: dbType, URL: dbURL})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}

	baseURL := strings.TrimSpace(os.Getenv("OPEN_LIBRARY_BASE_URL"))
	userAgent := strings.TrimSpace(os.Getenv("OPEN_LIBRARY_USER_AGENT"))
	if userAgent == "" {
		userAgent = "Atoman book catalog importer"
	}
	provider, err := books.NewOpenLibraryProvider(nil, baseURL, userAgent)
	if err != nil {
		log.Fatalf("configure Open Library provider: %v", err)
	}
	summary, err := books.NewCatalogImporter(db).ImportFromProvider(context.Background(), provider, *query, *limit)
	if err != nil {
		log.Fatalf("import Open Library records: %v", err)
	}
	log.Printf("imported %d records: new works=%d, new editions=%d, new people=%d, new contributions=%d; existing works=%d, editions=%d, people=%d, contributions=%d",
		summary.Records,
		summary.NewWorks,
		summary.NewEditions,
		summary.NewPeople,
		summary.NewContributions,
		summary.ExistingWorks,
		summary.ExistingEditions,
		summary.ExistingPeople,
		summary.ExistingContributions,
	)
}
