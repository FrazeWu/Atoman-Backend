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
	if os.Getenv("ATOMAN_MAIL_HISTORY_IMPORT_ENABLED") == "1" {
		jmapVault, e := mail.NewMailCredentialVault(os.Getenv("ATOMAN_MAIL_JMAP_ENCRYPTION_KEY_B64"))
		if e != nil {
			log.Fatal("Mail user credential vault unavailable")
		}
		jmap, e := mail.NewStalwartJMAPClient(os.Getenv("ATOMAN_MAIL_STALWART_URL"), nil)
		if e != nil {
			log.Fatal("Mail JMAP configuration unavailable")
		}
		reader := mail.NewMailReadService(db, jmap, jmapVault, true)
		history, e := mail.NewConfiguredExternalHistory(db, vault, reader)
		if e != nil {
			log.Fatal("Mail history import configuration unavailable")
		}
		importReport, e := mail.RunExternalHistoryDue(ctx, history)
		if e != nil {
			log.Fatal("Mail history worker failed")
		}
		if os.Getenv("ATOMAN_MAIL_TWO_WAY_FLAGS_ENABLED") == "1" {
			flagsReport, flagErr := mail.RunExternalFlagSyncDue(ctx, history)
			if flagErr != nil {
				log.Fatal("Two-way flag worker failed")
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"sync": report, "history": importReport, "flag_sync": flagsReport})
			return
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"sync": report, "history": importReport})
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
}
