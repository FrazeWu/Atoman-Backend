package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"atoman/internal/modules/music"
	"atoman/internal/storage"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	_ = godotenv.Load(".env.dev")
	_ = godotenv.Load(".env")
	if err := requiredWorkerConfig(); err != nil {
		log.Fatal(err)
	}
	if err := validateWorkerToolchain(music.NewSystemMediaCommandRunner()); err != nil {
		log.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(os.Getenv("DATABASE_URL")), &gorm.Config{})
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}
	client, err := storage.InitS3Client()
	if err != nil {
		log.Fatalf("initialize object store: %v", err)
	}
	store := music.NewMusicImportObjectStore(client)
	if store == nil {
		log.Fatal("MUSIC_SOURCE_BUCKET or S3_BUCKET is required")
	}
	worker := music.NewImportWorker(db, store, workerIDFromEnv())
	var processor music.ImportProcessor
	mediaStore := music.NewMusicImportMediaStore(client)
	if mediaStore == nil {
		log.Printf("music import worker: playback storage unavailable; queued jobs will not be claimed")
	} else {
		processor = music.NewMediaImportProcessor(db, mediaStore, music.NewSystemMediaCommandRunner(), os.Getenv("MUSIC_PLAYBACK_URL_PREFIX"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runWorker(ctx, worker, processor, workerPollIntervalFromEnv()); err != nil {
		log.Fatal(err)
	}
}

func validateWorkerToolchain(runner music.MediaCommandRunner) error {
	return music.ValidateMediaToolchain(runner)
}

type workerRunner interface {
	RunOnce(context.Context, music.ImportProcessor) (bool, error)
}

func runWorker(ctx context.Context, worker workerRunner, processor music.ImportProcessor, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}
	for {
		processed, err := worker.RunOnce(ctx, processor)
		if err != nil {
			log.Printf("music import worker: %v", err)
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pollInterval):
		}
	}
}

func workerPollIntervalFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("MUSIC_IMPORT_WORKER_POLL_INTERVAL"))
	if raw == "" {
		return 5 * time.Second
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval <= 0 {
		return 5 * time.Second
	}
	return interval
}

func requiredWorkerConfig() error {
	if strings.TrimSpace(os.Getenv("DATABASE_URL")) == "" {
		return errors.New("DATABASE_URL is required for music_import_worker")
	}
	if strings.TrimSpace(os.Getenv("MUSIC_SOURCE_BUCKET")) == "" && strings.TrimSpace(os.Getenv("S3_BUCKET")) == "" {
		return errors.New("MUSIC_SOURCE_BUCKET or S3_BUCKET is required for music_import_worker")
	}
	return nil
}

func workerIDFromEnv() string {
	if id := strings.TrimSpace(os.Getenv("MUSIC_IMPORT_WORKER_ID")); id != "" {
		return id
	}
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "music-import-worker"
	}
	return "music-import-" + host
}
