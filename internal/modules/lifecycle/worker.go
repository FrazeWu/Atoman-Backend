package lifecycle

import (
	"log"
	"os"
	"time"

	"gorm.io/gorm"
)

func StartWorker(db *gorm.DB) {
	if os.Getenv("CONTENT_LIFECYCLE_WORKER_ENABLED") == "false" {
		return
	}
	service := NewService(db)
	run := func() {
		now := time.Now().UTC()
		if err := service.PublishDue(now, 50); err != nil {
			log.Printf("content lifecycle scheduled publish failed: %v", err)
		}
		if err := service.DispatchPendingPublications(50); err != nil {
			log.Printf("content lifecycle publication dispatch failed: %v", err)
		}
	}
	go func() {
		run()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			run()
		}
	}()
}
