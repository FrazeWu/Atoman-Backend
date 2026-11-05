// Mail outbox worker. NEVER auto-started by the main server or deployment.
// Operators must first complete and document private R2, Stalwart, and
// TrueNAS restoration gates, then install a supervised worker unit.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/modules/mail"
)

func main() {
	once := flag.Bool("once", false, "process one bounded batch and stop")
	batch := flag.Int("batch", 20, "maximum messages claimed per batch (1..100)")
	flag.Parse()
	if *batch < 1 || *batch > 100 {
		log.Fatal("Invalid worker batch size")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" || os.Getenv("DATABASE_TYPE") == "" {
		log.Fatal("Mail worker database configuration is unavailable")
	}
	db, err := app.OpenDB(config.DBConfig{Type: os.Getenv("DATABASE_TYPE"), URL: dbURL})
	if err != nil {
		log.Fatal("Mail worker cannot open database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatal("Mail worker database session unavailable")
	}
	defer sqlDB.Close()
	sender, err := mail.NewConfiguredMailOutbox(db)
	if err != nil || sender == nil {
		log.Fatal("Mail worker operational gates are not satisfied")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	for {
		completed, err := sender.RunDue(ctx, *batch)
		if err != nil {
			log.Printf("Mail worker stopped: dispatch requires operator review (claimed: %d)", completed)
			os.Exit(1)
		}
		if *once {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
	}
}
