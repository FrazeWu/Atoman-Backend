package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/model"
	"atoman/internal/modules/music"

	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

func main() {
	envFile := flag.String("env", ".env.dev", "env file to load")
	workers := flag.Int("workers", 3, "number of songs to process concurrently")
	force := flag.Bool("force", false, "regenerate existing waveforms")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		log.Printf("WARN: load %s: %v", *envFile, err)
	}
	db, err := app.OpenDB(config.DBConfig{Type: os.Getenv("DATABASE_TYPE"), URL: os.Getenv("DATABASE_URL")})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	if !db.Migrator().HasColumn(&model.Song{}, "waveform_peaks") {
		log.Fatal("waveform_peaks column is missing; run cmd/migrate first")
	}
	if *workers < 1 {
		*workers = 1
	}
	if err := backfill(context.Background(), db, music.NewSystemMediaCommandRunner(), *workers, *force); err != nil {
		log.Fatal(err)
	}
}

func backfill(ctx context.Context, db *gorm.DB, runner music.MediaCommandRunner, workerCount int, force bool) error {
	var songs []model.Song
	if err := db.Select("id", "title", "audio_url", "waveform_peaks").Where("COALESCE(audio_url, '') <> ''").Find(&songs).Error; err != nil {
		return fmt.Errorf("list songs: %w", err)
	}

	jobs := make(chan model.Song)
	var completed atomic.Int64
	var failed atomic.Int64
	var skipped int64
	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for song := range jobs {
				peaks, err := music.GenerateWaveformPeaks(ctx, runner, resolveAudioURL(song.AudioURL))
				if err != nil {
					failed.Add(1)
					log.Printf("WARN: %s (%s): %v", song.Title, song.ID, err)
					continue
				}
				encoded, _ := json.Marshal(peaks)
				if err := db.WithContext(ctx).Model(&model.Song{}).Where("id = ?", song.ID).Update("waveform_peaks", encoded).Error; err != nil {
					failed.Add(1)
					log.Printf("WARN: update %s (%s): %v", song.Title, song.ID, err)
					continue
				}
				completed.Add(1)
			}
		}()
	}
	for _, song := range songs {
		if !force && hasCompleteWaveform(song.WaveformPeaks) {
			skipped++
			continue
		}
		jobs <- song
	}
	close(jobs)
	wg.Wait()
	log.Printf("waveform backfill complete: updated=%d skipped=%d failed=%d", completed.Load(), skipped, failed.Load())
	if failed.Load() > 0 {
		return fmt.Errorf("%d songs failed; rerun to retry them", failed.Load())
	}
	return nil
}

func hasCompleteWaveform(raw json.RawMessage) bool {
	var peaks []int
	return json.Unmarshal(raw, &peaks) == nil && len(peaks) == music.WaveformPeakCount
}

func resolveAudioURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	if strings.HasPrefix(raw, "/uploads/") {
		return strings.TrimRight(os.Getenv("PUBLIC_UPLOADS_BASE_URL"), "/") + raw
	}
	return strings.TrimRight(os.Getenv("S3_URL_PREFIX"), "/") + "/" + strings.TrimLeft(raw, "/")
}
