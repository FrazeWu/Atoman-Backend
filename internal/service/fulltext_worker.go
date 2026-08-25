package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"atoman/internal/model"

	"github.com/google/uuid"
)

const (
	FullTextWorkerEnabledDefault = true
	FullTextWorkerConcurrency    = 4
	FullTextWorkerTimeout        = 10 * time.Second
	FullTextWorkerMaxAttempts    = 4

	fullTextWorkerInterval              = 2 * time.Minute
	fullTextWorkerStartupDelay          = 120 * time.Second
	fullTextWorkerBatchSize             = 100
	fullTextStaleFetchAfter             = 20 * time.Minute
	fullTextMaxResponseBytes            = 5 * 1024 * 1024
	fullTextMaxRedirects                = 5
	fullTextRedirectLimitMessage        = "stopped after too many redirects"
	fullTextAutoDisableFailureThreshold = 3
)

var fullTextHTTPClient = &http.Client{
	Timeout:   FullTextWorkerTimeout,
	Transport: newFullTextSafeHTTPTransport(),
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
	Concurrency  int
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
		Concurrency:  parseEnvPositiveInt("FULLTEXT_WORKER_CONCURRENCY", FullTextWorkerConcurrency),
	}
}

var (
	fullTextWorkerWakeMu sync.RWMutex
	fullTextWorkerWake   chan struct{}
)

func registerFullTextWorkerWake(wake chan struct{}) {
	fullTextWorkerWakeMu.Lock()
	defer fullTextWorkerWakeMu.Unlock()
	fullTextWorkerWake = wake
}

func unregisterFullTextWorkerWake(wake chan struct{}) {
	fullTextWorkerWakeMu.Lock()
	defer fullTextWorkerWakeMu.Unlock()
	if fullTextWorkerWake == wake {
		fullTextWorkerWake = nil
	}
}

func FullTextWorkerEnvironmentEnabled() bool {
	return parseEnvBool("FULLTEXT_WORKER_ENABLED", FullTextWorkerEnabledDefault)
}

func RequestFullTextWorkerRun() bool {
	fullTextWorkerWakeMu.RLock()
	wake := fullTextWorkerWake
	fullTextWorkerWakeMu.RUnlock()
	if wake == nil {
		return false
	}
	select {
	case wake <- struct{}{}:
	default:
	}
	return true
}

func fullTextBatchSizeForSettings(cfg fullTextWorkerConfig, settings FeedFullTextSettings) int {
	if settings.ReaderCrawlBatchSize < cfg.BatchSize {
		return settings.ReaderCrawlBatchSize
	}
	return cfg.BatchSize
}

func runConfiguredFullTextCycle(db *gorm.DB, now time.Time, cfg fullTextWorkerConfig, settings FeedFullTextSettings) {
	if !settings.AutoSyncEnabled {
		return
	}
	if settings.ReaderCrawlEnabled {
		result, err := RunFeedReaderCrawl(db, settings, now)
		if err != nil {
			log.Printf("feed reader crawl preparation failed: %v", err)
		} else if result.Scanned > 0 {
			log.Printf("feed reader crawl prepared scanned=%d updated=%d requeued=%d skipped=%d", result.Scanned, result.Updated, result.Requeued, result.Skipped)
		}
	}
	runFullTextCycle(db, now, fullTextBatchSizeForSettings(cfg, settings), cfg.Concurrency)
}

func StartFullTextWorker(ctx context.Context, db *gorm.DB) <-chan struct{} {
	cfg := loadFullTextWorkerConfig()
	if !cfg.Enabled {
		log.Println("fulltext worker disabled by FULLTEXT_WORKER_ENABLED=false")
		done := make(chan struct{})
		close(done)
		return done
	}

	wake := make(chan struct{}, 1)
	registerFullTextWorkerWake(wake)

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer unregisterFullTextWorkerWake(wake)

		timer := time.NewTimer(cfg.StartupDelay)
		defer timer.Stop()
		var lastRun time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				now := time.Now().UTC()
				settings, err := LoadFeedFullTextSettings(db)
				if err != nil {
					log.Printf("fulltext worker load settings failed: %v", err)
					continue
				}
				runFullTextCycle(db, now, fullTextBatchSizeForSettings(cfg, settings), cfg.Concurrency)
				lastRun = now
			case now := <-timer.C:
				settings, err := LoadFeedFullTextSettings(db)
				if err != nil {
					log.Printf("fulltext worker load settings failed: %v", err)
				} else {
					interval := time.Duration(settings.AutoSyncIntervalMinute) * time.Minute
					if settings.AutoSyncEnabled && (lastRun.IsZero() || now.Sub(lastRun) >= interval) {
						runConfiguredFullTextCycle(db, now.UTC(), cfg, settings)
						lastRun = now
					}
				}
				timer.Reset(cfg.Interval)
			}
		}
	}()
	return done
}

type claimedFullTextItem struct {
	item       model.FeedItem
	source     model.FeedSource
	leaseToken string
}

func runFullTextCycle(db *gorm.DB, now time.Time, batchSize, concurrency int) {
	if err := recoverStaleFullTextFetches(db, now); err != nil {
		log.Printf("fulltext worker recover stale fetches failed: %v", err)
	}

	claimed, err := claimFullTextBatch(db, now, batchSize)
	if err != nil {
		log.Printf("fulltext worker batch claim failed: %v", err)
		return
	}
	if len(claimed) == 0 {
		return
	}
	if concurrency > len(claimed) {
		concurrency = len(claimed)
	}
	if concurrency < 1 {
		concurrency = 1
	}

	domainLocks := make(map[string]chan struct{}, len(claimed))
	sourceLocks := make(map[uuid.UUID]chan struct{}, len(claimed))
	sourceStates := make(map[uuid.UUID]*model.FeedSource, len(claimed))
	sourceLeaseTokens := make(map[uuid.UUID]string, len(claimed))
	for _, claimedItem := range claimed {
		key := fullTextDomainKey(claimedItem.item.Link)
		if _, exists := domainLocks[key]; !exists {
			domainLocks[key] = make(chan struct{}, 1)
		}
		if _, exists := sourceLocks[claimedItem.source.ID]; !exists {
			sourceLocks[claimedItem.source.ID] = make(chan struct{}, 1)
			sourceCopy := claimedItem.source
			sourceStates[claimedItem.source.ID] = &sourceCopy
		}
		sourceLeaseTokens[claimedItem.source.ID] = claimedItem.leaseToken
	}
	jobs := make(chan claimedFullTextItem)
	done := make(chan struct{}, concurrency)
	worker := func() {
		defer func() { done <- struct{}{} }()
		for claimedItem := range jobs {
			sourceLock := sourceLocks[claimedItem.source.ID]
			domainLock := domainLocks[fullTextDomainKey(claimedItem.item.Link)]
			sourceLock <- struct{}{}
			domainLock <- struct{}{}
			var processErr error
			currentSource := sourceStates[claimedItem.source.ID]
			if currentSource == nil {
				processErr = fmt.Errorf("full text source %s missing from claimed batch", claimedItem.source.ID)
			} else {
				hostNow := time.Now().UTC()
				hostLease, acquired, leaseErr := acquireFullTextHostLease(db, claimedItem.item.Link, hostNow)
				processErr = leaseErr
				if processErr == nil && !acquired {
					processErr = deferFullTextHostClaim(db, claimedItem.item, now)
				}
				if processErr == nil && acquired {
					processErr = processFullTextItem(db, &claimedItem.item, currentSource, now)
					if releaseErr := releaseFullTextHostLease(db, hostLease, time.Now().UTC()); releaseErr != nil {
						processErr = errors.Join(processErr, releaseErr)
					}
				}
			}
			<-domainLock
			<-sourceLock
			if processErr != nil {
				log.Printf("fulltext worker process failed for item %s: %v", claimedItem.item.ID, processErr)
			}
		}
	}
	launchFullTextWorkers(concurrency, worker)
	for _, claimedItem := range claimed {
		jobs <- claimedItem
	}
	close(jobs)
	for i := 0; i < concurrency; i++ {
		<-done
	}
	sourceIDs := make([]uuid.UUID, 0, len(sourceLeaseTokens))
	sharedToken := ""
	for sourceID, value := range sourceLeaseTokens {
		sourceIDs = append(sourceIDs, sourceID)
		sharedToken = value
	}
	if err := releaseFullTextSourceLeases(db, sourceIDs, sharedToken); err != nil {
		log.Printf("fulltext worker release source leases failed: %v", err)
	}
}

func launchFullTextWorkers(count int, worker func()) {
	if count <= 0 {
		return
	}
	go worker()
	launchFullTextWorkers(count-1, worker)
}

func fullTextDomainKey(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return rawURL
	}
	return strings.ToLower(parsed.Hostname())
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
	if reusable, found, err := findReusableFullText(db, *item); err != nil {
		return fmt.Errorf("find reusable full text: %w", err)
	} else if found {
		return markFullTextSuccess(db, item, source, reusable, now)
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
		if shouldTryFullTextRenderer(resp.StatusCode) {
			if renderedBody, attempted, rendererErr := fetchRenderedFullText(item.Link); attempted {
				if rendererErr == nil {
					return processFullTextDocument(db, item, source, renderedBody, now, false)
				}
				log.Printf("fulltext renderer fallback failed for item %s: %v", item.ID, sanitizeRSSLogError(rendererErr))
			}
		}
		return markFullTextFailure(db, item, source, FullTextErrorHTTPStatus, fmt.Sprintf("status=%d", resp.StatusCode), now)
	}
	if contentType := strings.ToLower(resp.Header.Get("Content-Type")); !strings.Contains(contentType, "text/html") {
		if renderedBody, attempted, rendererErr := fetchRenderedFullText(item.Link); attempted {
			if rendererErr == nil {
				return processFullTextDocument(db, item, source, renderedBody, now, false)
			}
			log.Printf("fulltext renderer fallback failed for item %s: %v", item.ID, sanitizeRSSLogError(rendererErr))
		}
		return markFullTextFailure(db, item, source, FullTextErrorNonHTML, contentType, now)
	}

	body, tooLarge, err := readFullTextResponseBody(resp.Body)
	if err != nil {
		return markFullTextFailure(db, item, source, FullTextErrorRequestFailed, err.Error(), now)
	}
	if tooLarge {
		return markFullTextFailure(db, item, source, FullTextErrorResponseTooLarge, "response too large", now)
	}

	return processFullTextDocument(db, item, source, body, now, true)
}

func shouldTryFullTextRenderer(statusCode int) bool {
	return statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || statusCode == http.StatusTooManyRequests
}

func processFullTextDocument(db *gorm.DB, item *model.FeedItem, source *model.FeedSource, body []byte, now time.Time, allowRenderer bool) error {
	if metadata, metadataErr := ExtractFeedImageMetadata(item.Link, bytes.NewReader(body)); metadataErr == nil {
		if err := persistFeedImageMetadata(db, item, source, metadata); err != nil {
			log.Printf("fulltext worker image fallback failed for item %s: %v", item.ID, err)
		}
	}

	result, errCode, err := ExtractAndSanitizeFullText(item.Link, bytes.NewReader(body))
	if err != nil && allowRenderer {
		if renderedBody, attempted, rendererErr := fetchRenderedFullText(item.Link); attempted {
			if rendererErr == nil {
				return processFullTextDocument(db, item, source, renderedBody, now, false)
			}
			log.Printf("fulltext renderer fallback failed for item %s: %v", item.ID, sanitizeRSSLogError(rendererErr))
		}
	}
	if err != nil {
		if errCode == "" {
			errCode = FullTextErrorRequestFailed
		}
		return markFullTextFailure(db, item, source, errCode, err.Error(), now)
	}
	return markFullTextSuccess(db, item, source, result, now)
}

func persistFeedImageMetadata(db *gorm.DB, item *model.FeedItem, source *model.FeedSource, metadata FeedImageMetadata) error {
	fallbackURL := firstNonEmpty(metadata.ImageURL, metadata.IconURL)
	if fallbackURL == "" {
		return nil
	}

	if strings.TrimSpace(item.ImageURL) == "" {
		if err := db.Model(&model.FeedItem{}).Where("id = ?", item.ID).Update("image_url", fallbackURL).Error; err != nil {
			return err
		}
		item.ImageURL = fallbackURL
	}

	if source != nil && strings.TrimSpace(source.CoverURL) == "" {
		if err := db.Model(&model.FeedSource{}).Where("id = ?", source.ID).Update("cover_url", fallbackURL).Error; err != nil {
			return err
		}
		source.CoverURL = fallbackURL
	}
	return nil
}

func markFullTextSuccess(db *gorm.DB, item *model.FeedItem, source *model.FeedSource, result FullTextResult, now time.Time) error {
	pageCandidate := ReaderCandidate{
		HTML:           result.HTML,
		Source:         ReaderSourcePage,
		QualityScore:   result.QualityScore,
		QualityFlags:   result.QualityFlags,
		CharacterCount: result.WordCount,
		ContentHash:    hashReaderContent(result.HTML),
		Extractor:      result.Extractor,
	}
	if pageCandidate.QualityScore == 0 {
		if rescored, err := sanitizeReaderFragment(item.Link, result.HTML); err == nil {
			pageCandidate = rescored
			pageCandidate.Source = ReaderSourcePage
			pageCandidate.Extractor = firstNonEmpty(result.Extractor, "legacy")
		}
	}
	var feedCandidate ReaderCandidate
	if candidate, err := SanitizeFeedContent(item.Link, item.FeedContentHTML); err == nil {
		feedCandidate = candidate
	}
	selected := ChooseReaderCandidate(feedCandidate, pageCandidate)

	item.FullTextStatus = FullTextStatusSuccess
	item.FullTextURLHash = fullTextURLHash(item.Link)
	item.FullTextHTML = result.HTML
	item.FullTextWordCount = result.WordCount
	item.FullTextFetchedAt = &now
	item.FullTextErrorCode = ""
	item.FullTextError = ""
	item.NextFullTextAttemptAt = nil
	item.ReaderHTML = selected.HTML
	item.ReaderSource = selected.Source
	item.ReaderQualityScore = selected.QualityScore
	item.ReaderQualityFlags = ReaderQualityFlagsJSON(selected.QualityFlags)
	item.ReaderVersion = ReaderVersionCurrent
	item.ReaderContentHash = selected.ContentHash

	if err := db.Model(&model.FeedItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"full_text_status":          item.FullTextStatus,
		"full_text_url_hash":        item.FullTextURLHash,
		"full_text_html":            item.FullTextHTML,
		"full_text_word_count":      item.FullTextWordCount,
		"full_text_fetched_at":      item.FullTextFetchedAt,
		"full_text_error_code":      "",
		"full_text_error":           "",
		"next_full_text_attempt_at": nil,
		"reader_html":               item.ReaderHTML,
		"reader_source":             item.ReaderSource,
		"reader_quality_score":      item.ReaderQualityScore,
		"reader_quality_flags":      item.ReaderQualityFlags,
		"reader_version":            item.ReaderVersion,
		"reader_content_hash":       item.ReaderContentHash,
	}).Error; err != nil {
		return err
	}

	source.FullTextConsecutiveFailureCount = 0
	source.FullTextLastSuccessAt = &now
	source.FullTextLastErrorCode = ""
	source.FullTextLastError = ""
	if err := db.Model(&model.FeedSource{}).Where("id = ?", source.ID).Updates(map[string]any{
		"full_text_success_count":             gorm.Expr("full_text_success_count + ?", 1),
		"full_text_consecutive_failure_count": 0,
		"full_text_last_success_at":           source.FullTextLastSuccessAt,
		"full_text_last_error_code":           "",
		"full_text_last_error":                "",
	}).Error; err != nil {
		return err
	}
	diagnosticKind := "recovered"
	diagnosticMessage := "full text fetch recovered"
	if result.Extractor == "reused" {
		diagnosticKind = "reused"
		diagnosticMessage = "full text reused from an existing feed item"
	}
	return recordFeedSourceDiagnostic(db, source.ID, &item.ID, diagnosticKind, "", diagnosticMessage, item.FullTextAttemptCount, &now)
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

	source.FullTextConsecutiveFailureCount++
	source.FullTextLastFailureAt = &now
	source.FullTextLastErrorCode = errorCode
	source.FullTextLastError = errorMessage
	if shouldAutoDisableFullTextSource(errorCode, source.FullTextConsecutiveFailureCount) {
		source.FullTextEnabled = false
	}

	if err := db.Model(&model.FeedSource{}).Where("id = ?", source.ID).Updates(map[string]any{
		"full_text_enabled":                   source.FullTextEnabled,
		"full_text_failure_count":             gorm.Expr("full_text_failure_count + ?", 1),
		"full_text_consecutive_failure_count": gorm.Expr("full_text_consecutive_failure_count + ?", 1),
		"full_text_last_failure_at":           source.FullTextLastFailureAt,
		"full_text_last_error_code":           source.FullTextLastErrorCode,
		"full_text_last_error":                source.FullTextLastError,
	}).Error; err != nil {
		return err
	}
	return recordFeedSourceDiagnostic(db, source.ID, &item.ID, "failure", errorCode, errorMessage, item.FullTextAttemptCount, nil)
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
	if err := db.Model(&model.FeedItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"full_text_status":          item.FullTextStatus,
		"full_text_html":            "",
		"full_text_word_count":      0,
		"full_text_fetched_at":      nil,
		"full_text_error_code":      "",
		"full_text_error":           "",
		"next_full_text_attempt_at": nil,
	}).Error; err != nil {
		return fmt.Errorf("disable full text item %s: %w", item.ID, err)
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
