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
	limit := flag.Int("limit", 0, "maximum number of dump records to import; 0 imports all (API mode defaults to 20)")
	worksDump := flag.String("works-dump", "", "Open Library works dump (.txt or .gz)")
	editionsDump := flag.String("editions-dump", "", "Open Library editions dump (.txt or .gz)")
	authorsDump := flag.String("authors-dump", "", "Open Library authors dump (.txt or .gz)")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		log.Printf("WARN: load %s: %v", *envFile, err)
	}
	dumpMode := strings.TrimSpace(*worksDump) != "" || strings.TrimSpace(*editionsDump) != "" || strings.TrimSpace(*authorsDump) != ""
	if dumpMode && (strings.TrimSpace(*worksDump) == "" || strings.TrimSpace(*editionsDump) == "" || strings.TrimSpace(*authorsDump) == "") {
		log.Fatal("-works-dump, -editions-dump and -authors-dump must be provided together")
	}
	if !dumpMode && strings.TrimSpace(*query) == "" {
		log.Fatal("-query is required unless dump paths are provided")
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

	if dumpMode {
		summary, err := books.NewOpenLibraryDumpImporter(db).ImportChinese(context.Background(), books.OpenLibraryDumpImportOptions{
			WorksPath: *worksDump, EditionsPath: *editionsDump, AuthorsPath: *authorsDump, Limit: *limit,
		})
		if err != nil {
			log.Fatalf("import Chinese Open Library dumps: %v", err)
		}
		log.Printf("imported %d Chinese works (%d editions scanned, %d Chinese editions, %d works scanned, %d authors selected); new works=%d, new editions=%d, new people=%d, new contributions=%d; existing works=%d, editions=%d, people=%d, contributions=%d",
			summary.RecordsImported, summary.EditionsScanned, summary.ChineseEditions, summary.WorksScanned, summary.AuthorsSelected,
			summary.NewWorks, summary.NewEditions, summary.NewPeople, summary.NewContributions,
			summary.ExistingWorks, summary.ExistingEditions, summary.ExistingPeople, summary.ExistingContributions,
		)
		return
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
	queryLimit := *limit
	if queryLimit == 0 {
		queryLimit = 20
	}
	summary, err := books.NewCatalogImporter(db).ImportFromProvider(context.Background(), provider, *query, queryLimit)
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
