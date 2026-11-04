package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/model"
	"atoman/internal/modules/music"

	"github.com/joho/godotenv"
)

func main() {
	envFile := flag.String("env", ".env.dev", "path to env file")
	dryRun := flag.Bool("dry-run", false, "dry run without updating database")
	limit := flag.Int("limit", 100, "maximum songs to process")
	flag.Parse()

	_ = godotenv.Load(*envFile)
	_ = godotenv.Load(".env")

	db, err := app.OpenDB(config.DBConfig{
		Type: strings.TrimSpace(os.Getenv("DATABASE_TYPE")),
		URL:  strings.TrimSpace(os.Getenv("DATABASE_URL")),
	})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}

	runner := music.NewSystemMediaCommandRunner()
	if err := music.ValidateMediaToolchain(runner); err != nil {
		log.Fatalf("validate media toolchain: %v", err)
	}

	var songs []model.Song
	query := db.Where("deleted_at IS NULL AND (duration_sec = 0 OR waveform_peaks = '[]'::jsonb OR waveform_peaks IS NULL)").
		Order("created_at ASC").
		Limit(*limit)
	if err := query.Find(&songs).Error; err != nil {
		log.Fatalf("load songs: %v", err)
	}

	log.Printf("found %d songs needing duration or waveform backfill", len(songs))
	ctx := context.Background()
	updated := 0

	for _, song := range songs {
		audioURL := strings.TrimSpace(song.AudioURL)
		if audioURL == "" {
			continue
		}
		// If URL is on localhost, ensure it is accessible
		parsedURL, err := url.Parse(audioURL)
		if err != nil {
			log.Printf("skip song %s: invalid audio URL %s", song.ID, audioURL)
			continue
		}

		// 1. Probe duration with ffprobe if duration_sec == 0
		durationSec := song.DurationSec
		if durationSec <= 0 {
			probeRaw, probeErr := runner.Run(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", parsedURL.String())
			if probeErr == nil {
				var seconds float64
				if _, scanErr := fmt.Sscanf(strings.TrimSpace(string(probeRaw)), "%f", &seconds); scanErr == nil && seconds > 0 {
					durationSec = int(seconds + 0.5)
				}
			}
		}

		// 2. Generate waveform peaks if empty
		waveformJSON := song.WaveformPeaks
		var existingPeaks []int
		_ = json.Unmarshal(waveformJSON, &existingPeaks)
		if len(existingPeaks) != music.WaveformPeakCount {
			peaks, waveErr := music.GenerateWaveformPeaks(ctx, runner, parsedURL.String())
			if waveErr != nil {
				log.Printf("warn: waveform generation failed for song %s (%s): %v", song.ID, song.Title, waveErr)
			} else if len(peaks) == music.WaveformPeakCount {
				marshaled, _ := json.Marshal(peaks)
				waveformJSON = marshaled
			}
		}

		if durationSec == song.DurationSec && len(waveformJSON) == len(song.WaveformPeaks) {
			continue
		}

		log.Printf("updating song %s (%q): duration=%ds, peaks_len=%d", song.ID, song.Title, durationSec, len(waveformJSON))
		if !*dryRun {
			updates := map[string]any{
				"duration_sec":   durationSec,
				"waveform_peaks": waveformJSON,
				"updated_at":     time.Now(),
			}
			if err := db.Model(&model.Song{}).Where("id = ?", song.ID).Updates(updates).Error; err != nil {
				log.Printf("error updating song %s: %v", song.ID, err)
				continue
			}
		}
		updated++
	}

	log.Printf("successfully backfilled %d / %d songs (dry_run=%v)", updated, len(songs), *dryRun)
}
