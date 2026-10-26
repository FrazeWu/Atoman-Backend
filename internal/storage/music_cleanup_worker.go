package storage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"atoman/internal/model"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	musicCleanupBatchSize     = 8
	musicCleanupLeaseDuration = 3 * time.Minute
)

var musicCleanupDatabase atomic.Pointer[gorm.DB]

// ConfigureMusicCleanupQueue installs the DB-backed fallback used by all
// existing best-effort music object deletion call sites in this process.
func ConfigureMusicCleanupQueue(db *gorm.DB) {
	musicCleanupDatabase.Store(db)
}

func enqueueFailedMusicObjectDeletion(key string) error {
	db := musicCleanupDatabase.Load()
	if db == nil {
		return errors.New("music object cleanup database is not configured")
	}
	if len(key) > 1024 {
		return fmt.Errorf("music object key exceeds cleanup queue limit")
	}
	return db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).
		Create(&model.MusicCleanupJob{Key: key, NextAttemptAt: time.Now().UTC()}).Error
}

func StartMusicCleanupWorker(ctx context.Context, db *gorm.DB, client *s3.S3) <-chan struct{} {
	done := make(chan struct{})
	ConfigureMusicCleanupQueue(db)
	if db == nil || client == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		run := func() {
			for ctx.Err() == nil {
				processed, err := RunMusicObjectCleanupOnce(ctx, db, client)
				if err != nil {
					log.Printf("WARN: music S3 cleanup worker: %v", err)
					return
				}
				if !processed {
					return
				}
			}
		}
		run()
		ticker := time.NewTicker(30 * time.Second)
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

// RunMusicObjectCleanupOnce is safe across multiple worker instances.
// Claiming is atomic and stale leases may be reclaimed after a crash.
func RunMusicObjectCleanupOnce(ctx context.Context, db *gorm.DB, client *s3.S3) (bool, error) {
	if db == nil || client == nil {
		return false, nil
	}
	now := time.Now().UTC()
	token := uuid.NewString()
	var jobs []model.MusicCleanupJob
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("next_attempt_at <= ? AND (lease_until IS NULL OR lease_until <= ?)", now, now).
			Order("next_attempt_at ASC, key ASC").Limit(musicCleanupBatchSize).
			Find(&jobs).Error; err != nil {
			return err
		}
		for _, job := range jobs {
			if err := tx.Model(&model.MusicCleanupJob{}).Where("key = ?", job.Key).
				Updates(map[string]any{"lease_token": token, "lease_until": now.Add(musicCleanupLeaseDuration)}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || len(jobs) == 0 {
		return false, err
	}
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	var failed error
	for _, job := range jobs {
		deleteCtx, cancelDelete := context.WithTimeout(ctx, 15*time.Second)
		_, deleteErr := client.DeleteObjectWithContext(deleteCtx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(job.Key)})
		cancelDelete()
		writeCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if deleteErr == nil {
			err = db.WithContext(writeCtx).Where("key = ? AND lease_token = ?", job.Key, token).Delete(&model.MusicCleanupJob{}).Error
		} else {
			delay := time.Second
			for i := 0; i < job.Attempts && delay < time.Minute; i++ {
				delay *= 2
			}
			if delay > time.Minute {
				delay = time.Minute
			}
			err = db.WithContext(writeCtx).Model(&model.MusicCleanupJob{}).
				Where("key = ? AND lease_token = ?", job.Key, token).
				Updates(map[string]any{
					"lease_token": "", "lease_until": nil,
					"attempts": job.Attempts + 1, "next_attempt_at": time.Now().UTC().Add(delay),
					"last_error": deleteErr.Error(),
				}).Error
			failed = errors.Join(failed, fmt.Errorf("delete music object %s: %w", job.Key, deleteErr))
		}
		stop()
		if err != nil {
			failed = errors.Join(failed, fmt.Errorf("persist cleanup state %s: %w", job.Key, err))
		}
	}
	return true, failed
}
