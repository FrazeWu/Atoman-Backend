package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"atoman/internal/model"

	"gorm.io/gorm"
)

const feedReaderCrawlStatusKey = "feed.reader.crawl.status"

var feedReaderCrawlMutex sync.Mutex

type FeedReaderCrawlStatus struct {
	LastRunAt time.Time `json:"last_run_at"`
	Scanned   int       `json:"scanned"`
	Updated   int       `json:"updated"`
	Requeued  int       `json:"requeued"`
	Skipped   int       `json:"skipped"`
}

func RunFeedReaderCrawl(db *gorm.DB, settings FeedFullTextSettings, now time.Time) (FeedReaderBackfillResult, error) {
	feedReaderCrawlMutex.Lock()
	defer feedReaderCrawlMutex.Unlock()

	publishedAfter := now.UTC().AddDate(0, 0, -settings.ReaderCrawlDays)
	result, err := BackfillFeedReaderContent(db, FeedReaderBackfillOptions{
		Limit:          settings.ReaderCrawlBatchSize,
		PublishedAfter: publishedAfter,
		Apply:          true,
		Requeue:        true,
	})
	if err != nil {
		return result, fmt.Errorf("prepare feed reader crawl: %w", err)
	}
	status := FeedReaderCrawlStatus{
		LastRunAt: now.UTC(),
		Scanned:   result.Scanned,
		Updated:   result.Updated,
		Requeued:  result.Requeued,
		Skipped:   result.Skipped,
	}
	value, err := json.Marshal(status)
	if err != nil {
		return result, fmt.Errorf("encode feed reader crawl status: %w", err)
	}
	if err := db.Save(&model.SiteSetting{
		Key:         feedReaderCrawlStatusKey,
		Value:       string(value),
		Description: "Feed reader crawl status",
	}).Error; err != nil {
		return result, fmt.Errorf("save feed reader crawl status: %w", err)
	}
	return result, nil
}

func LoadFeedReaderCrawlStatus(db *gorm.DB) (FeedReaderCrawlStatus, error) {
	var stored model.SiteSetting
	if err := db.First(&stored, "key = ?", feedReaderCrawlStatusKey).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return FeedReaderCrawlStatus{}, nil
		}
		return FeedReaderCrawlStatus{}, fmt.Errorf("load feed reader crawl status: %w", err)
	}
	var status FeedReaderCrawlStatus
	if err := json.Unmarshal([]byte(stored.Value), &status); err != nil {
		return FeedReaderCrawlStatus{}, fmt.Errorf("decode feed reader crawl status: %w", err)
	}
	return status, nil
}

func CountFeedReaderCrawlCandidates(db *gorm.DB, settings FeedFullTextSettings, now time.Time) (int64, error) {
	options := FeedReaderBackfillOptions{
		PublishedAfter: now.UTC().AddDate(0, 0, -settings.ReaderCrawlDays),
		Requeue:        true,
	}
	var count int64
	if err := feedReaderBackfillQuery(db, options).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count feed reader crawl candidates: %w", err)
	}
	return count, nil
}
