package service

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"atoman/internal/model"
)

const (
	FullTextWorkerEnabledDefault = true
	FullTextWorkerConcurrency    = 4
	FullTextWorkerTimeout        = 10 * time.Second
	FullTextWorkerMaxAttempts    = 4

	fullTextWorkerInterval              = 2 * time.Minute
	fullTextWorkerStartupDelay          = 120 * time.Second
	fullTextWorkerBatchSize             = 4
	fullTextStaleFetchAfter             = 20 * time.Minute
	fullTextMaxResponseBytes            = 5 * 1024 * 1024
	fullTextMaxRedirects                = 5
	fullTextRedirectLimitMessage        = "stopped after too many redirects"
	fullTextAutoDisableFailureThreshold = 3
)

var fullTextHTTPClient = &http.Client{
	Timeout: FullTextWorkerTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= fullTextMaxRedirects {
			return errors.New(fullTextRedirectLimitMessage)
		}
		return ValidateFullTextTargetURL(req.URL.String())
	},
}

type fullTextWorkerConfig struct {
	Enabled      bool
	StartupDelay time.Duration
	Interval     time.Duration
	BatchSize    int
}

func parseEnvPositiveInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		log.Printf("WARN: invalid %s=%q; using default %d", name, raw, fallback)
		return fallback
	}
	return value
}

func loadFullTextWorkerConfig() fullTextWorkerConfig {
	return fullTextWorkerConfig{
		Enabled:      parseEnvBool("FULLTEXT_WORKER_ENABLED", FullTextWorkerEnabledDefault),
		StartupDelay: parseEnvDuration("FULLTEXT_WORKER_STARTUP_DELAY", fullTextWorkerStartupDelay),
		Interval:     parseEnvDuration("FULLTEXT_WORKER_INTERVAL", fullTextWorkerInterval),
		BatchSize:    parseEnvPositiveInt("FULLTEXT_WORKER_BATCH_SIZE", fullTextWorkerBatchSize),
	}
}

func StartFullTextWorker(db *gorm.DB) {
	cfg := loadFullTextWorkerConfig()
	if !cfg.Enabled {
		log.Println("fulltext worker disabled by FULLTEXT_WORKER_ENABLED=false")
		return
	}

	go func() {
		time.Sleep(cfg.StartupDelay)
		runFullTextCycle(db, time.Now(), cfg.BatchSize)

		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for range ticker.C {
			runFullTextCycle(db, time.Now(), cfg.BatchSize)
		}
	}()
}

func runFullTextCycle(db *gorm.DB, now time.Time, batchSize int) {
	if err := recoverStaleFullTextFetches(db, now); err != nil {
		log.Printf("fulltext worker recover stale fetches failed: %v", err)
	}

	for i := 0; i < batchSize; i++ {
		item, source, ok, err := claimNextFullTextItem(db, now)
		if err != nil {
			log.Printf("fulltext worker claim failed: %v", err)
			return
		}
		if !ok {
			return
		}
		if err := processFullTextItem(db, &item, &source, now); err != nil {
			log.Printf("fulltext worker process failed for item %s: %v", item.ID, err)
		}
	}
}

func claimNextFullTextItem(db *gorm.DB, now time.Time) (model.FeedItem, model.FeedSource, bool, error) {
	for {
		var candidates []model.FeedItem
		query := db.Preload("FeedSource").
			Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
			Where("feed_items.full_text_status IN ?", []string{FullTextStatusPending, FullTextStatusRetry}).
			Where("feed_items.next_full_text_attempt_at IS NULL OR feed_items.next_full_text_attempt_at <= ?", now).
			Where("feed_sources.source_type = ?", "external_rss").
			Where("feed_sources.full_text_enabled = ?", true).
			Where("feed_sources.rss_url NOT LIKE ?", "%/feed/rss/%").
			Where("COALESCE(feed_items.enclosure_url, '') = ''").
			Where("COALESCE(feed_items.enclosure_type, '') NOT LIKE ?", "audio/%").
			Where("COALESCE(feed_items.enclosure_type, '') NOT LIKE ?", "video/%").
			Where("COALESCE(feed_items.duration, '') = ''").
			Order("feed_items.published_at DESC")
		result := query.Limit(1).Find(&candidates)
		if result.Error != nil {
			return model.FeedItem{}, model.FeedSource{}, false, result.Error
		}
		if result.RowsAffected == 0 {
			return model.FeedItem{}, model.FeedSource{}, false, nil
		}
		candidate := candidates[0]
		if candidate.FeedSource == nil {
			return model.FeedItem{}, model.FeedSource{}, false, fmt.Errorf("feed item %s missing feed source", candidate.ID)
		}

		oldStatus := candidate.FullTextStatus
		attemptCount := candidate.FullTextAttemptCount + 1
		updates := map[string]any{
			"full_text_status":          FullTextStatusFetching,
			"full_text_attempt_count":   attemptCount,
			"last_full_text_attempt_at": &now,
			"next_full_text_attempt_at": nil,
		}
		updateResult := db.Model(&model.FeedItem{}).
			Where("id = ? AND full_text_status = ?", candidate.ID, oldStatus).
			Updates(updates)
		if updateResult.Error != nil {
			return model.FeedItem{}, model.FeedSource{}, false, updateResult.Error
		}
		if updateResult.RowsAffected == 0 {
			continue
		}

		candidate.FullTextStatus = FullTextStatusFetching
		candidate.FullTextAttemptCount = attemptCount
		candidate.LastFullTextAttemptAt = &now
		candidate.NextFullTextAttemptAt = nil
		return candidate, *candidate.FeedSource, true, nil
	}
}

func processFullTextItem(db *gorm.DB, item *model.FeedItem, source *model.FeedSource, now time.Time) error {
	if source == nil {
		return fmt.Errorf("missing source")
	}
	if !source.FullTextEnabled {
		return markFullTextDisabled(db, item)
	}
	if err := ValidateFullTextTargetURL(item.Link); err != nil {
		return markFullTextDisabled(db, item)
	}

	resp, err := fetchFullTextResponse(item.Link)
	if err != nil {
		errorCode := FullTextErrorRequestFailed
		switch {
		case isTooManyRedirectsError(err):
			errorCode = FullTextErrorTooManyRedirects
		case isTimeoutError(err):
			errorCode = FullTextErrorRequestTimeout
		}
		return markFullTextFailure(db, item, source, errorCode, err.Error(), now)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return markFullTextFailure(db, item, source, FullTextErrorHTTPStatus, fmt.Sprintf("status=%d", resp.StatusCode), now)
	}
	if contentType := strings.ToLower(resp.Header.Get("Content-Type")); !strings.Contains(contentType, "text/html") {
		return markFullTextFailure(db, item, source, FullTextErrorNonHTML, contentType, now)
	}

	body, tooLarge, err := readFullTextResponseBody(resp.Body)
	if err != nil {
		return markFullTextFailure(db, item, source, FullTextErrorRequestFailed, err.Error(), now)
	}
	if tooLarge {
		return markFullTextFailure(db, item, source, FullTextErrorResponseTooLarge, "response too large", now)
	}

	result, errCode, err := ExtractAndSanitizeFullText(item.Link, strings.NewReader(string(body)))
	if err != nil {
		if errCode == "" {
			errCode = FullTextErrorRequestFailed
		}
		return markFullTextFailure(db, item, source, errCode, err.Error(), now)
	}
	return markFullTextSuccess(db, item, source, result, now)
}

func markFullTextSuccess(db *gorm.DB, item *model.FeedItem, source *model.FeedSource, result FullTextResult, now time.Time) error {
	item.FullTextStatus = FullTextStatusSuccess
	item.FullTextHTML = result.HTML
	item.FullTextWordCount = result.WordCount
	item.FullTextFetchedAt = &now
	item.FullTextErrorCode = ""
	item.FullTextError = ""
	item.NextFullTextAttemptAt = nil

	if err := db.Model(&model.FeedItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"full_text_status":          item.FullTextStatus,
		"full_text_html":            item.FullTextHTML,
		"full_text_word_count":      item.FullTextWordCount,
		"full_text_fetched_at":      item.FullTextFetchedAt,
		"full_text_error_code":      "",
		"full_text_error":           "",
		"next_full_text_attempt_at": nil,
	}).Error; err != nil {
		return err
	}

	source.FullTextSuccessCount++
	source.FullTextLastSuccessAt = &now
	source.FullTextLastErrorCode = ""
	source.FullTextLastError = ""
	return db.Model(&model.FeedSource{}).Where("id = ?", source.ID).Updates(map[string]any{
		"full_text_success_count":   source.FullTextSuccessCount,
		"full_text_last_success_at": source.FullTextLastSuccessAt,
		"full_text_last_error_code": "",
		"full_text_last_error":      "",
	}).Error
}

func markFullTextFailure(db *gorm.DB, item *model.FeedItem, source *model.FeedSource, errorCode, errorMessage string, now time.Time) error {
	nextAttemptAt, terminal := CalculateNextFullTextRetryAt(now, item.FullTextAttemptCount)
	item.FullTextErrorCode = errorCode
	item.FullTextError = errorMessage
	item.NextFullTextAttemptAt = nil
	if terminal {
		item.FullTextStatus = FullTextStatusFailed
	} else {
		item.FullTextStatus = FullTextStatusRetry
		item.NextFullTextAttemptAt = &nextAttemptAt
	}

	if err := db.Model(&model.FeedItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"full_text_status":          item.FullTextStatus,
		"full_text_error_code":      item.FullTextErrorCode,
		"full_text_error":           item.FullTextError,
		"next_full_text_attempt_at": item.NextFullTextAttemptAt,
	}).Error; err != nil {
		return err
	}

	source.FullTextFailureCount++
	source.FullTextLastFailureAt = &now
	source.FullTextLastErrorCode = errorCode
	source.FullTextLastError = errorMessage
	if shouldAutoDisableFullTextSource(errorCode, source.FullTextFailureCount) {
		source.FullTextEnabled = false
	}

	return db.Model(&model.FeedSource{}).Where("id = ?", source.ID).Updates(map[string]any{
		"full_text_enabled":         source.FullTextEnabled,
		"full_text_failure_count":   source.FullTextFailureCount,
		"full_text_last_failure_at": source.FullTextLastFailureAt,
		"full_text_last_error_code": source.FullTextLastErrorCode,
		"full_text_last_error":      source.FullTextLastError,
	}).Error
}

func shouldAutoDisableFullTextSource(errorCode string, failureCount int) bool {
	if failureCount < fullTextAutoDisableFailureThreshold {
		return false
	}
	switch errorCode {
	case FullTextErrorLoginWallDetected, FullTextErrorExtractTooShort, FullTextErrorSanitizeEmpty:
		return true
	default:
		return false
	}
}

func markFullTextDisabled(db *gorm.DB, item *model.FeedItem) error {
	item.FullTextStatus = FullTextStatusDisabled
	item.FullTextHTML = ""
	item.FullTextWordCount = 0
	item.FullTextFetchedAt = nil
	item.FullTextErrorCode = ""
	item.FullTextError = ""
	item.NextFullTextAttemptAt = nil
	return db.Model(&model.FeedItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"full_text_status":          item.FullTextStatus,
		"full_text_html":            "",
		"full_text_word_count":      0,
		"full_text_fetched_at":      nil,
		"full_text_error_code":      "",
		"full_text_error":           "",
		"next_full_text_attempt_at": nil,
	}).Error
}

func recoverStaleFullTextFetches(db *gorm.DB, now time.Time) error {
	staleBefore := now.Add(-fullTextStaleFetchAfter)

	var staleItems []model.FeedItem
	if err := db.Preload("FeedSource").
		Where("full_text_status = ?", FullTextStatusFetching).
		Where("last_full_text_attempt_at IS NULL OR last_full_text_attempt_at <= ?", staleBefore).
		Find(&staleItems).Error; err != nil {
		return err
	}

	for _, item := range staleItems {
		nextAttemptAt, terminal := CalculateNextFullTextRetryAt(now, item.FullTextAttemptCount)
		item.FullTextErrorCode = FullTextErrorRequestTimeout
		item.FullTextError = "stale full text fetch recovered"
		item.NextFullTextAttemptAt = nil
		if terminal {
			item.FullTextStatus = FullTextStatusFailed
		} else {
			item.FullTextStatus = FullTextStatusRetry
			item.NextFullTextAttemptAt = &nextAttemptAt
		}
		if err := db.Model(&model.FeedItem{}).Where("id = ?", item.ID).Updates(map[string]any{
			"full_text_status":          item.FullTextStatus,
			"full_text_error_code":      item.FullTextErrorCode,
			"full_text_error":           item.FullTextError,
			"next_full_text_attempt_at": item.NextFullTextAttemptAt,
		}).Error; err != nil {
			return err
		}
		if item.FeedSource != nil {
			if err := markFullTextFailure(db, &item, item.FeedSource, FullTextErrorRequestTimeout, "stale full text fetch recovered", now); err != nil {
				return err
			}
		}
	}
	return nil
}

func fetchFullTextResponse(targetURL string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "AtomanFullTextBot/1.0")
	return fullTextHTTPClient.Do(req)
}

func readFullTextResponseBody(body io.Reader) ([]byte, bool, error) {
	limited := io.LimitReader(body, fullTextMaxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > fullTextMaxResponseBytes {
		return nil, true, nil
	}
	return data, false, nil
}

func isTimeoutError(err error) bool {
	type timeout interface{ Timeout() bool }
	var target timeout
	return errors.As(err, &target) && target.Timeout()
}

func isTooManyRedirectsError(err error) bool {
	return err != nil && strings.Contains(err.Error(), fullTextRedirectLimitMessage)
}
