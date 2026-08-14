package music

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const defaultMusicImportWorkerPollInterval = 5 * time.Second

// StartImportWorker starts media processing only when S3-backed imports are available.
func StartImportWorker(ctx context.Context, db *gorm.DB, s3Client *s3.S3) <-chan struct{} {
	done := make(chan struct{})
	if db == nil || s3Client == nil || !strings.EqualFold(strings.TrimSpace(os.Getenv("STORAGE_TYPE")), "s3") {
		close(done)
		return done
	}

	mediaStore := NewMusicImportMediaStore(s3Client)
	if mediaStore == nil {
		log.Println("music import worker disabled: media storage is unavailable")
		close(done)
		return done
	}

	workerID := strings.TrimSpace(os.Getenv("MUSIC_IMPORT_WORKER_ID"))
	if workerID == "" {
		workerID, _ = os.Hostname()
	}
	if workerID == "" {
		workerID = "music-import-worker"
	}

	playbackURLPrefix := strings.TrimRight(strings.TrimSpace(os.Getenv("MUSIC_PLAYBACK_URL_PREFIX")), "/")
	if playbackURLPrefix == "" {
		playbackURLPrefix = strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
	}
	processor := NewMediaImportProcessor(db, mediaStore, NewSystemMediaCommandRunner(), playbackURLPrefix)
	if userAgent := strings.TrimSpace(os.Getenv("MUSICBRAINZ_USER_AGENT")); userAgent != "" {
		processor.WithMetadataEnricher(NewExternalAlbumMetadataEnricher(
			&http.Client{Timeout: 10 * time.Second},
			envOrDefault("MUSICBRAINZ_BASE_URL", "https://musicbrainz.org"),
			envOrDefault("COVER_ART_ARCHIVE_BASE_URL", "https://coverartarchive.org"),
			envOrDefault("LRCLIB_BASE_URL", "https://lrclib.net"),
			userAgent,
		))
	} else {
		log.Println("music metadata enrichment disabled: MUSICBRAINZ_USER_AGENT is empty")
	}
	importService := NewServiceWithS3(db, s3Client)
	worker := NewImportWorker(db, NewMusicImportObjectStore(s3Client), workerID).WithCompletionFinalizer(
		func(_ context.Context, importID uuid.UUID) error {
			return importService.FinalizeSubmittedAlbumImport(importID)
		},
	).WithMediaService(importService)

	interval := defaultMusicImportWorkerPollInterval
	if raw := strings.TrimSpace(os.Getenv("MUSIC_IMPORT_WORKER_POLL_INTERVAL")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			interval = parsed
		} else {
			log.Printf("WARN: invalid MUSIC_IMPORT_WORKER_POLL_INTERVAL %q; using %s", raw, interval)
		}
	}

	go func() {
		defer close(done)
		log.Printf("music import worker started as %s", workerID)
		run := func() {
			if err := importService.CleanupExpiredMusicAssetUploads(ctx); err != nil {
				log.Printf("WARN: music import worker could not clean expired audio uploads: %v", err)
			}

			finalized, finalizeErr := worker.FinalizeSubmittedReady(ctx)
			if finalizeErr != nil {
				log.Printf("WARN: music import worker could not finalize ready imports: %v", finalizeErr)
			}
			if finalized > 0 {
				log.Printf("music import worker finalized %d ready import(s)", finalized)
			}

			processed, err := worker.RunOnce(ctx, processor)
			if err != nil {
				log.Printf("WARN: music import worker failed: %v", err)
				return
			}
			if processed {
				log.Printf("music import worker processed an import")
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

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
