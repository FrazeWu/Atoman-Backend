package books

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/service/s3"
	"gorm.io/gorm"
)

const (
	defaultBookProcessingWorkerPollInterval = 5 * time.Second
	defaultBookRetentionSweepInterval       = 24 * time.Hour
)

// StartProcessingWorker runs private book processing only with S3-backed storage.
func StartProcessingWorker(ctx context.Context, db *gorm.DB, s3Client *s3.S3) <-chan struct{} {
	done := make(chan struct{})
	if db == nil || s3Client == nil || !strings.EqualFold(strings.TrimSpace(os.Getenv("STORAGE_TYPE")), "s3") {
		close(done)
		return done
	}
	store := newS3BookUploadStore(s3Client)
	if store == nil {
		log.Println("book processing worker disabled: storage is unavailable")
		close(done)
		return done
	}
	service := NewService(db).WithBookUploadStore(store)
	if binary := strings.TrimSpace(os.Getenv("BOOK_CLAMSCAN_PATH")); binary != "" {
		scanner, err := NewClamAVBookVirusScanner(binary)
		if err != nil {
			log.Printf("WARN: book virus scanner disabled: %v", err)
		} else {
			service.WithVirusScanner(scanner)
		}
	}
	interval := defaultBookProcessingWorkerPollInterval
	if raw := strings.TrimSpace(os.Getenv("BOOK_PROCESSING_WORKER_POLL_INTERVAL")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			interval = parsed
		} else {
			log.Printf("WARN: invalid BOOK_PROCESSING_WORKER_POLL_INTERVAL %q; using %s", raw, interval)
		}
	}

	go func() {
		defer close(done)
		log.Printf("book processing worker started with %s poll interval", interval)
		retentionSweepAt := time.Time{}
		run := func() {
			processed, err := service.ProcessBookAssets(ctx)
			if err != nil {
				log.Printf("WARN: book processing worker failed: %v", err)
			}
			if processed > 0 {
				log.Printf("book processing worker processed %d asset(s)", processed)
			}
			cleaned, cleanupErr := service.ProcessBookCleanup(ctx)
			if cleanupErr != nil {
				log.Printf("WARN: book cleanup worker failed: %v", cleanupErr)
			}
			if cleaned > 0 {
				log.Printf("book cleanup worker removed %d import object(s)", cleaned)
			}
			if retentionSweepAt.IsZero() || !time.Now().UTC().Before(retentionSweepAt) {
				retained, retentionErr := service.ProcessBookRetention(ctx)
				if retentionErr != nil {
					log.Printf("WARN: book retention worker failed: %v", retentionErr)
				}
				if retained > 0 {
					log.Printf("book retention worker removed materials for %d request(s)", retained)
				}
				retentionSweepAt = time.Now().UTC().Add(defaultBookRetentionSweepInterval)
			}
		}
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
