// mail_followup_worker persists owner-scoped mail reminders after their due
// time, including when no browser tab is open. It never reads Stalwart admin
// secrets and never includes private message contents in notifications.
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
	if os.Getenv("ATOMAN_MAIL_FOLLOWUP_ENABLED") != "1" {
		log.Fatal("Mail follow-up worker disabled")
	}
	if os.Getenv("DATABASE_URL") == "" || os.Getenv("DATABASE_TYPE") == "" {
		log.Fatal("Database configuration unavailable")
	}
	vault, err := mail.NewMailOutboxVault(os.Getenv("ATOMAN_MAIL_OUTBOX_ENCRYPTION_KEY_B64"))
	if err != nil {
		log.Fatal("Mail reminder encryption vault unavailable")
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
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	report, err := mail.RunMailFollowUpDue(ctx, db, vault, time.Now().UTC())
	if err != nil {
		log.Fatal("Mail follow-up dispatch failed")
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
}
