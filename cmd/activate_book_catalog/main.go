package main

import (
	"flag"
	"log"
	"os"
	"strings"

	"atoman/internal/app"
	"atoman/internal/config"

	"github.com/joho/godotenv"
)

func main() {
	envFile := flag.String("env", ".env.dev", "environment file")
	apply := flag.Bool("apply", false, "activate imported book metadata")
	flag.Parse()
	if err := godotenv.Load(*envFile); err != nil {
		log.Printf("WARN: load %s: %v", *envFile, err)
	}
	if !*apply {
		log.Print("dry run only; rerun with -apply to activate Open Library metadata")
		return
	}
	db, err := app.OpenDB(config.DBConfig{Type: strings.TrimSpace(os.Getenv("DATABASE_TYPE")), URL: strings.TrimSpace(os.Getenv("DATABASE_URL"))})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	updates := []struct {
		table      string
		targetType string
	}{
		{table: "book_works", targetType: "book_work"},
		{table: "book_editions", targetType: "book_edition"},
		{table: "book_people", targetType: "book_person"},
	}
	for _, update := range updates {
		editStatus := ""
		if update.table != "book_people" {
			editStatus = ", edit_status = CASE WHEN edit_status = 'closed' THEN 'development' ELSE edit_status END"
		}
		result := db.Exec("UPDATE "+update.table+" SET lifecycle_status = 'active'"+editStatus+" WHERE lifecycle_status = 'draft' AND id IN (SELECT target_id FROM book_sources WHERE target_type = ? AND kind LIKE 'open_library_%' AND deleted_at IS NULL)", update.targetType)
		if result.Error != nil {
			log.Fatalf("activate %s: %v", update.table, result.Error)
		}
		log.Printf("activated %s: %d", update.table, result.RowsAffected)
	}
}
