package main

import (
	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/modules/mail"
	"context"
	"encoding/json"
	"log"
	"os"
	"time"
)

func main() {
	if os.Getenv("ATOMAN_MAIL_EXTERNAL_IMAP_ENABLED") != "1" {
		log.Fatal("External IMAP synchronization is disabled")
	}
	if os.Getenv("DATABASE_URL") == "" || os.Getenv("DATABASE_TYPE") == "" {
		log.Fatal("Database configuration unavailable")
	}
	vault, err := mail.NewMailOutboxVault(os.Getenv("ATOMAN_MAIL_OUTBOX_ENCRYPTION_KEY_B64"))
	if err != nil {
		log.Fatal("External mail encryption vault unavailable")
	}
	db, err := app.OpenDB(config.DBConfig{Type: os.Getenv("DATABASE_TYPE"), URL: os.Getenv("DATABASE_URL")})
	if err != nil {
		log.Fatal("Database unavailable")
	}
	sql, err := db.DB()
	if err != nil {
		log.Fatal("Database session unavailable")
	}
	defer sql.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	report, err := mail.RunExternalSyncDue(ctx, db, vault, nil)
	if err != nil {
		log.Fatal("External IMAP synchronization failed")
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
}
