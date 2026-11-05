// Invoke from a supervised systemd timer; no side effects without the explicit
// cleanup flag and verified mail configuration.
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
	if os.Getenv("ATOMAN_MAIL_CLEANUP_ENABLED") != "1" {
		log.Fatal("Mail cleanup is disabled")
	}
	if os.Getenv("DATABASE_URL") == "" || os.Getenv("DATABASE_TYPE") == "" {
		log.Fatal("Database configuration unavailable")
	}
	db, e := app.OpenDB(config.DBConfig{Type: os.Getenv("DATABASE_TYPE"), URL: os.Getenv("DATABASE_URL")})
	if e != nil {
		log.Fatal("Database unavailable")
	}
	sql, e := db.DB()
	if e != nil {
		log.Fatal("Database session unavailable")
	}
	defer sql.Close()
	workspace, sender, e := mail.NewConfiguredMailWorkspace(db)
	if e != nil {
		log.Fatal("Mail operational gates unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, e := workspace.RunMaintenance(ctx, sender)
	if e != nil {
		log.Fatal("Mail maintenance failed; rerun after checking services")
	}
	json.NewEncoder(os.Stdout).Encode(result)
}
