package lifecycle

import (
	"context"
	"log"
	"os"
	"time"

	"gorm.io/gorm"
)

func StartWorker(ctx context.Context, db *gorm.DB) <-chan struct{} {
	if os.Getenv("CONTENT_LIFECYCLE_WORKER_ENABLED") == "false" {
		done := make(chan struct{})
		close(done)
		return done
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
	return startPeriodicLifecycleWorker(ctx, 30*time.Second, run)
}

func startPeriodicLifecycleWorker(ctx context.Context, interval time.Duration, run func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		run()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	return done
}
