package indexnow

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	indexNowDurableBatchSize = 100
	indexNowLeaseDuration    = 45 * time.Second
)

// enqueueDurable writes the notification before returning to the publisher.
// Re-enqueueing a URL increments Revision: an in-flight acknowledgement must
// never delete a newer change to the same URL.
func (w *Worker) enqueueDurable(paths []string) error {
	if w.db == nil {
		return errors.New("IndexNow durable queue requires PostgreSQL")
	}
	urls, err := w.submitter.normalizeURLs(paths)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	items := make([]model.IndexNowSubmission, 0, len(urls))
	for _, url := range urls {
		if len(url) > 1900 {
			return fmt.Errorf("IndexNow URL exceeds durable queue length limit")
		}
		items = append(items, model.IndexNowSubmission{URL: url, Revision: 1, NextAttemptAt: now})
	}
	if len(items) == 0 {
		return nil
	}
	return w.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "url"}},
		DoUpdates: clause.Assignments(map[string]any{
			"revision":        gorm.Expr("indexnow_submissions.revision + 1"),
			"next_attempt_at": now,
			"updated_at":      now,
		}),
	}).Create(&items).Error
}

func (w *Worker) runDurable(ctx context.Context) {
	interval := time.NewTicker(time.Second)
	defer interval.Stop()
	run := func() {
		for ctx.Err() == nil {
			handled, err := w.runDurableOnce(ctx)
			if err != nil {
				log.Printf("WARN: IndexNow durable queue submission failed: %v", err)
				return
			}
			if !handled {
				return
			}
		}
	}
	run() // Restart recovery: queued rows survive process termination.
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.queue:
			run()
		case <-interval.C:
			run()
		}
	}
}

func (w *Worker) runDurableOnce(ctx context.Context) (bool, error) {
	now := time.Now().UTC()
	token := uuid.NewString()
	var claimed []model.IndexNowSubmission
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("next_attempt_at <= ? AND (lease_until IS NULL OR lease_until <= ?)", now, now).
			Order("next_attempt_at ASC, url ASC").Limit(indexNowDurableBatchSize).
			Find(&claimed).Error; err != nil {
			return err
		}
		for _, item := range claimed {
			if err := tx.Model(&model.IndexNowSubmission{}).Where("url = ?", item.URL).Updates(map[string]any{
				"lease_token": token, "lease_until": now.Add(indexNowLeaseDuration),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || len(claimed) == 0 {
		return false, err
	}
	urls := make([]string, 0, len(claimed))
	for _, item := range claimed {
		urls = append(urls, item.URL)
	}
	submitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	submitErr := w.submitter.Submit(submitCtx, urls)
	cancel()

	// Persistence must finish even when the server is shutting down.
	writeCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	writeErr := w.db.WithContext(writeCtx).Transaction(func(tx *gorm.DB) error {
		for _, item := range claimed {
			if submitErr == nil {
				removed := tx.Where("url = ? AND lease_token = ? AND revision = ?", item.URL, token, item.Revision).
					Delete(&model.IndexNowSubmission{})
				if removed.Error != nil {
					return removed.Error
				}
				if removed.RowsAffected == 0 {
					// Another publisher enqueued the same URL during delivery.
					if err := tx.Model(&model.IndexNowSubmission{}).
						Where("url = ? AND lease_token = ?", item.URL, token).
						Updates(map[string]any{
							"lease_token": "", "lease_until": nil, "next_attempt_at": time.Now().UTC(),
						}).Error; err != nil {
						return err
					}
				}
				continue
			}
			delay := time.Second
			for i := 0; i < item.Attempts && delay < time.Minute; i++ {
				delay *= 2
			}
			if delay > time.Minute {
				delay = time.Minute
			}
			if err := tx.Model(&model.IndexNowSubmission{}).
				Where("url = ? AND lease_token = ?", item.URL, token).
				Updates(map[string]any{
					"lease_token": "", "lease_until": nil,
					"next_attempt_at": time.Now().UTC().Add(delay),
					"attempts":        item.Attempts + 1,
					"last_error":      submitErr.Error(),
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return true, errors.Join(submitErr, writeErr)
}
