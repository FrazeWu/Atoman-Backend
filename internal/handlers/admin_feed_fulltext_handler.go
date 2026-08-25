package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/service"
)

type adminFeedFullTextSourceRow struct {
	ID                     uuid.UUID  `json:"id"`
	Title                  string     `json:"title"`
	RssURL                 string     `json:"rss_url"`
	FullTextEnabled        bool       `json:"full_text_enabled"`
	Hidden                 bool       `json:"hidden"`
	SuccessCount           int64      `json:"success_count"`
	RetryCount             int64      `json:"retry_count"`
	FailedCount            int64      `json:"failed_count"`
	PendingCount           int64      `json:"pending_count"`
	SuccessRate            float64    `json:"success_rate"`
	ReaderReadyCount       int64      `json:"reader_ready_count"`
	ReaderQualityPassCount int64      `json:"reader_quality_pass_count"`
	SummaryFallbackCount   int64      `json:"summary_fallback_count"`
	ReaderQualityPassRate  float64    `json:"reader_quality_pass_rate"`
	Status                 string     `json:"status"`
	LastSuccessAt          *time.Time `json:"last_success_at"`
	LastFailureAt          *time.Time `json:"last_failure_at"`
	LastErrorCode          string     `json:"last_error_code"`
	LastError              string     `json:"last_error"`
}

type adminFeedFullTextSourceAggregate struct {
	SourceID               uuid.UUID `gorm:"column:source_id"`
	SuccessCount           int64     `gorm:"column:success_count"`
	RetryCount             int64     `gorm:"column:retry_count"`
	FailedCount            int64     `gorm:"column:failed_count"`
	PendingCount           int64     `gorm:"column:pending_count"`
	ReaderReadyCount       int64     `gorm:"column:reader_ready_count"`
	ReaderQualityPassCount int64     `gorm:"column:reader_quality_pass_count"`
	SummaryFallbackCount   int64     `gorm:"column:summary_fallback_count"`
}

type adminFeedFullTextItemRow struct {
	ID                    uuid.UUID       `json:"id"`
	Title                 string          `json:"title"`
	Link                  string          `json:"link"`
	SourceID              uuid.UUID       `json:"source_id"`
	SourceTitle           string          `json:"source_title"`
	FullTextStatus        string          `json:"full_text_status"`
	FullTextAttemptCount  int             `json:"attempt_count"`
	FullTextErrorCode     string          `json:"error_code"`
	FullTextError         string          `json:"error_message"`
	LastFullTextAttemptAt *time.Time      `json:"last_attempt_at"`
	NextFullTextAttemptAt *time.Time      `json:"next_attempt_at"`
	PublishedAt           time.Time       `json:"published_at"`
	ReaderSource          string          `json:"reader_source"`
	ReaderQualityScore    int             `json:"reader_quality_score"`
	ReaderQualityFlags    json.RawMessage `json:"reader_quality_flags"`
	ReaderVersion         int             `json:"reader_version"`
}

func parseAdminListParams(c *gin.Context) (page int, limit int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ = strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return page, limit
}

func adminFullTextBlogSourceQuery(db *gorm.DB) *gorm.DB {
	return db.Model(&model.FeedSource{}).
		Where("source_type = ?", "external_rss").
		Where("rss_url NOT LIKE ?", "%/feed/rss/%").
		Where(`NOT EXISTS (
			SELECT 1 FROM feed_items source_items
			WHERE source_items.feed_source_id = feed_sources.id
		) OR EXISTS (
			SELECT 1 FROM feed_items blog_items
			WHERE blog_items.feed_source_id = feed_sources.id
				AND COALESCE(blog_items.enclosure_url, '') = ''
				AND COALESCE(blog_items.enclosure_type, '') NOT LIKE 'audio/%'
				AND COALESCE(blog_items.enclosure_type, '') NOT LIKE 'video/%'
				AND COALESCE(blog_items.duration, '') = ''
		)`)
}

func adminFullTextBlogItemQuery(db *gorm.DB) *gorm.DB {
	return db.Model(&model.FeedItem{}).
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_sources.source_type = ?", "external_rss").
		Where("feed_sources.rss_url NOT LIKE ?", "%/feed/rss/%").
		Where("COALESCE(feed_items.enclosure_url, '') = ''").
		Where("COALESCE(feed_items.enclosure_type, '') NOT LIKE ?", "audio/%").
		Where("COALESCE(feed_items.enclosure_type, '') NOT LIKE ?", "video/%").
		Where("COALESCE(feed_items.duration, '') = ''")
}

func adminFeedFullTextHealthStatus(enabled bool, pendingCount, retryCount, failedCount, successCount int64) string {
	if !enabled {
		return "disabled"
	}
	totalCompleted := successCount + failedCount
	failureRate := 0.0
	if totalCompleted > 0 {
		failureRate = float64(failedCount) / float64(totalCompleted)
	}
	switch {
	case failedCount > 0 || failureRate > 0.4:
		return "failing"
	case retryCount > 0 || pendingCount >= 5 || failureRate >= 0.1:
		return "degraded"
	default:
		return "healthy"
	}
}

func GetAdminFeedFullTextHealth(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var enabledSources int64
		var disabledSources int64
		var pendingItems int64
		var readyItems int64
		var fetchingItems int64
		var retryItems int64
		var successItems int64
		var failedItems int64
		var readerReadyItems int64
		var readerQualityPassItems int64
		var readerFeedItems int64
		var readerPageItems int64
		var readerSummaryItems int64
		var pendingOverSevenDays int64
		var activeSourceLeases int64
		var activeHostLeases int64
		var reusedItems int64
		var latestSuccessAt *time.Time
		var latestFailureAt *time.Time
		var oldestPendingItem model.FeedItem

		externalItems := func() *gorm.DB { return adminFullTextBlogItemQuery(db) }
		externalSources := func() *gorm.DB { return adminFullTextBlogSourceQuery(db) }

		externalSources().Where("full_text_enabled = ?", true).Count(&enabledSources)
		externalSources().Where("full_text_enabled = ?", false).Count(&disabledSources)
		externalSources().Where("full_text_lease_until > ?", time.Now().UTC()).Count(&activeSourceLeases)
		if db.Migrator().HasTable(&model.FeedFullTextHost{}) {
			db.Model(&model.FeedFullTextHost{}).Where("lease_until > ?", time.Now().UTC()).Count(&activeHostLeases)
		}
		db.Model(&model.FeedSourceDiagnostic{}).
			Joins("JOIN feed_sources ON feed_sources.id = feed_source_diagnostics.feed_source_id").
			Where("feed_sources.source_type = ? AND feed_source_diagnostics.kind = ?", "external_rss", "reused").
			Count(&reusedItems)
		externalItems().Where("feed_items.full_text_status = ?", service.FullTextStatusPending).Count(&pendingItems)
		externalItems().Where("feed_items.full_text_status IN ? AND (feed_items.next_full_text_attempt_at IS NULL OR feed_items.next_full_text_attempt_at <= ?)", []string{service.FullTextStatusPending, service.FullTextStatusRetry}, time.Now().UTC()).Count(&readyItems)
		externalItems().Where("feed_items.full_text_status = ?", service.FullTextStatusFetching).Count(&fetchingItems)
		externalItems().Where("feed_items.full_text_status = ?", service.FullTextStatusRetry).Count(&retryItems)
		externalItems().Where("feed_items.full_text_status = ?", service.FullTextStatusSuccess).Count(&successItems)
		externalItems().Where("feed_items.full_text_status = ?", service.FullTextStatusFailed).Count(&failedItems)
		externalItems().Where("COALESCE(feed_items.reader_html, '') <> ''").Count(&readerReadyItems)
		externalItems().Where("COALESCE(feed_items.reader_html, '') <> '' AND feed_items.reader_quality_score >= ?", service.ReaderQualityReadyThreshold).Count(&readerQualityPassItems)
		externalItems().Where("feed_items.reader_source = ?", service.ReaderSourceFeed).Count(&readerFeedItems)
		externalItems().Where("feed_items.reader_source = ?", service.ReaderSourcePage).Count(&readerPageItems)
		externalItems().Where("feed_items.reader_source = ? OR COALESCE(feed_items.reader_html, '') = ''", service.ReaderSourceSummary).Count(&readerSummaryItems)
		externalItems().Where("feed_items.full_text_status IN ? AND feed_items.created_at < ?", []string{service.FullTextStatusPending, service.FullTextStatusRetry}, time.Now().UTC().Add(-7*24*time.Hour)).Count(&pendingOverSevenDays)
		externalItems().Select("feed_items.full_text_fetched_at").Where("feed_items.full_text_fetched_at IS NOT NULL").Order("feed_items.full_text_fetched_at DESC").Limit(1).Scan(&latestSuccessAt)
		externalSources().Select("full_text_last_failure_at").Where("full_text_last_failure_at IS NOT NULL").Order("full_text_last_failure_at DESC").Limit(1).Scan(&latestFailureAt)
		externalItems().
			Where("feed_items.full_text_status = ?", service.FullTextStatusPending).
			Order("feed_items.created_at ASC").
			First(&oldestPendingItem)

		totalCompleted := successItems + failedItems
		successRate := 0.0
		if totalCompleted > 0 {
			successRate = float64(successItems) / float64(totalCompleted)
		}

		readerQualityPassRate := 0.0
		if readerReadyItems > 0 {
			readerQualityPassRate = float64(readerQualityPassItems) / float64(readerReadyItems)
		}
		type feedbackCount struct {
			Kind  string `gorm:"column:kind"`
			Count int64  `gorm:"column:count"`
		}
		var feedbackRows []feedbackCount
		if err := db.Model(&model.FeedContentFeedback{}).
			Select("feed_content_feedback.kind, COUNT(*) AS count").
			Joins("JOIN feed_items ON feed_items.id = feed_content_feedback.feed_item_id").
			Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
			Where("feed_sources.source_type = ?", "external_rss").
			Group("feed_content_feedback.kind").Scan(&feedbackRows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to aggregate reader feedback"})
			return
		}
		feedbackCounts := map[string]int64{"missing": 0, "layout": 0, "image": 0, "noise": 0}
		for _, row := range feedbackRows {
			feedbackCounts[row.Kind] = row.Count
		}

		settings, err := service.LoadFeedFullTextSettings(db)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load reader crawl settings"})
			return
		}
		readerCrawlPending, err := service.CountFeedReaderCrawlCandidates(db, settings, time.Now().UTC())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count reader crawl candidates"})
			return
		}
		readerCrawlStatus, err := service.LoadFeedReaderCrawlStatus(db)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load reader crawl status"})
			return
		}

		payload := gin.H{
			"enabled_sources":            enabledSources,
			"disabled_sources":           disabledSources,
			"pending_items":              pendingItems,
			"ready_items":                readyItems,
			"fetching_items":             fetchingItems,
			"retry_items":                retryItems,
			"success_items":              successItems,
			"failed_items":               failedItems,
			"reader_ready_items":         readerReadyItems,
			"reader_quality_pass_items":  readerQualityPassItems,
			"reader_quality_pass_rate":   readerQualityPassRate,
			"reader_feed_items":          readerFeedItems,
			"reader_page_items":          readerPageItems,
			"reader_summary_items":       readerSummaryItems,
			"pending_over_7d":            pendingOverSevenDays,
			"active_source_leases":       activeSourceLeases,
			"active_host_leases":         activeHostLeases,
			"reused_items":               reusedItems,
			"feedback_counts":            feedbackCounts,
			"reader_crawl_pending":       readerCrawlPending,
			"reader_crawl_last_scanned":  readerCrawlStatus.Scanned,
			"reader_crawl_last_updated":  readerCrawlStatus.Updated,
			"reader_crawl_last_requeued": readerCrawlStatus.Requeued,
			"reader_crawl_last_skipped":  readerCrawlStatus.Skipped,
			"success_rate":               successRate,
			"enabled":                    service.FullTextWorkerEnvironmentEnabled() && settings.AutoSyncEnabled,
			"concurrency":                service.FullTextWorkerConcurrency,
			"timeout_seconds":            int(service.FullTextWorkerTimeout / time.Second),
			"max_attempts":               service.FullTextWorkerMaxAttempts,
		}
		if !readerCrawlStatus.LastRunAt.IsZero() {
			payload["reader_crawl_last_run_at"] = readerCrawlStatus.LastRunAt
		}
		if latestSuccessAt != nil {
			payload["latest_success_at"] = *latestSuccessAt
		}
		if latestFailureAt != nil {
			payload["latest_failure_at"] = *latestFailureAt
		}
		if oldestPendingItem.ID != uuid.Nil {
			payload["oldest_pending_at"] = oldestPendingItem.CreatedAt
		}
		c.JSON(http.StatusOK, payload)
	}
}

func GetAdminFeedFullTextSources(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, limit := parseAdminListParams(c)
		offset := (page - 1) * limit
		q := strings.TrimSpace(c.Query("q"))
		enabled := strings.TrimSpace(c.Query("enabled"))
		status := strings.TrimSpace(c.Query("status"))
		hidden := strings.TrimSpace(c.Query("hidden"))
		sortKey := strings.TrimSpace(c.DefaultQuery("sort", "title"))

		query := adminFullTextBlogSourceQuery(db)
		if q != "" {
			like := "%" + q + "%"
			query = query.Where("title LIKE ? OR rss_url LIKE ?", like, like)
		}
		if enabled == "true" {
			query = query.Where("full_text_enabled = ?", true)
		} else if enabled == "false" {
			query = query.Where("full_text_enabled = ?", false)
		}
		if hidden == "true" {
			query = query.Where("hidden = ?", true)
		} else if hidden == "false" {
			query = query.Where("hidden = ?", false)
		}

		var sources []model.FeedSource
		if err := query.Find(&sources).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch full text sources"})
			return
		}

		aggregatesBySource := make(map[uuid.UUID]adminFeedFullTextSourceAggregate, len(sources))
		if len(sources) > 0 {
			sourceIDs := make([]uuid.UUID, 0, len(sources))
			for _, source := range sources {
				sourceIDs = append(sourceIDs, source.ID)
			}
			var aggregates []adminFeedFullTextSourceAggregate
			if err := adminFullTextBlogItemQuery(db).
				Select(`feed_items.feed_source_id AS source_id,
					SUM(CASE WHEN feed_items.full_text_status = 'success' THEN 1 ELSE 0 END) AS success_count,
					SUM(CASE WHEN feed_items.full_text_status = 'retry' THEN 1 ELSE 0 END) AS retry_count,
					SUM(CASE WHEN feed_items.full_text_status = 'failed' THEN 1 ELSE 0 END) AS failed_count,
					SUM(CASE WHEN feed_items.full_text_status = 'pending' THEN 1 ELSE 0 END) AS pending_count,
					SUM(CASE WHEN COALESCE(feed_items.reader_html, '') <> '' THEN 1 ELSE 0 END) AS reader_ready_count,
					SUM(CASE WHEN COALESCE(feed_items.reader_html, '') <> '' AND feed_items.reader_quality_score >= ? THEN 1 ELSE 0 END) AS reader_quality_pass_count,
					SUM(CASE WHEN feed_items.reader_source = 'summary' OR COALESCE(feed_items.reader_html, '') = '' THEN 1 ELSE 0 END) AS summary_fallback_count`, service.ReaderQualityReadyThreshold).
				Where("feed_items.feed_source_id IN ?", sourceIDs).
				Group("feed_items.feed_source_id").
				Scan(&aggregates).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to aggregate full text sources"})
				return
			}
			for _, aggregate := range aggregates {
				aggregatesBySource[aggregate.SourceID] = aggregate
			}
		}

		rows := make([]adminFeedFullTextSourceRow, 0, len(sources))
		for _, source := range sources {
			aggregate := aggregatesBySource[source.ID]
			completed := aggregate.SuccessCount + aggregate.FailedCount
			successRate := 0.0
			if completed > 0 {
				successRate = float64(aggregate.SuccessCount) / float64(completed)
			}
			readerQualityPassRate := 0.0
			if aggregate.ReaderReadyCount > 0 {
				readerQualityPassRate = float64(aggregate.ReaderQualityPassCount) / float64(aggregate.ReaderReadyCount)
			}
			row := adminFeedFullTextSourceRow{
				ID:                     source.ID,
				Title:                  source.Title,
				RssURL:                 source.RssURL,
				FullTextEnabled:        source.FullTextEnabled,
				Hidden:                 source.Hidden,
				SuccessCount:           aggregate.SuccessCount,
				RetryCount:             aggregate.RetryCount,
				FailedCount:            aggregate.FailedCount,
				PendingCount:           aggregate.PendingCount,
				SuccessRate:            successRate,
				ReaderReadyCount:       aggregate.ReaderReadyCount,
				ReaderQualityPassCount: aggregate.ReaderQualityPassCount,
				SummaryFallbackCount:   aggregate.SummaryFallbackCount,
				ReaderQualityPassRate:  readerQualityPassRate,
				Status:                 adminFeedFullTextHealthStatus(source.FullTextEnabled, aggregate.PendingCount, aggregate.RetryCount, aggregate.FailedCount, aggregate.SuccessCount),
				LastSuccessAt:          source.FullTextLastSuccessAt,
				LastFailureAt:          source.FullTextLastFailureAt,
				LastErrorCode:          source.FullTextLastErrorCode,
				LastError:              source.FullTextLastError,
			}
			if status != "" && row.Status != status {
				continue
			}
			rows = append(rows, row)
		}

		sort.Slice(rows, func(i, j int) bool {
			switch sortKey {
			case "last_failure_at":
				left := time.Time{}
				right := time.Time{}
				if rows[i].LastFailureAt != nil {
					left = *rows[i].LastFailureAt
				}
				if rows[j].LastFailureAt != nil {
					right = *rows[j].LastFailureAt
				}
				if !left.Equal(right) {
					return left.After(right)
				}
			case "failure_rate":
				leftCompleted := rows[i].SuccessCount + rows[i].FailedCount
				rightCompleted := rows[j].SuccessCount + rows[j].FailedCount
				leftRate := 0.0
				rightRate := 0.0
				if leftCompleted > 0 {
					leftRate = float64(rows[i].FailedCount) / float64(leftCompleted)
				}
				if rightCompleted > 0 {
					rightRate = float64(rows[j].FailedCount) / float64(rightCompleted)
				}
				if leftRate != rightRate {
					return leftRate > rightRate
				}
			case "pending_count":
				if rows[i].PendingCount != rows[j].PendingCount {
					return rows[i].PendingCount > rows[j].PendingCount
				}
			}
			return rows[i].Title < rows[j].Title
		})

		total := len(rows)
		if offset > total {
			offset = total
		}
		end := offset + limit
		if end > total {
			end = total
		}
		pagedRows := rows[offset:end]

		c.JSON(http.StatusOK, gin.H{
			"data": pagedRows,
			"meta": gin.H{
				"total": total,
				"page":  page,
				"limit": limit,
			},
		})
	}
}

func GetAdminFeedFullTextItems(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, limit := parseAdminListParams(c)
		offset := (page - 1) * limit
		sourceID := strings.TrimSpace(c.Query("source_id"))
		status := strings.TrimSpace(c.Query("status"))
		errorCode := strings.TrimSpace(c.Query("error_code"))
		q := strings.TrimSpace(c.Query("q"))
		sort := strings.TrimSpace(c.DefaultQuery("sort", "published_at"))

		query := adminFullTextBlogItemQuery(db)
		if sourceID != "" {
			query = query.Where("feed_items.feed_source_id = ?", sourceID)
		}
		if status != "" {
			query = query.Where("feed_items.full_text_status = ?", status)
		}
		if errorCode != "" {
			query = query.Where("feed_items.full_text_error_code = ?", errorCode)
		}
		if q != "" {
			like := "%" + q + "%"
			query = query.Where("feed_items.title LIKE ? OR feed_items.link LIKE ? OR feed_sources.title LIKE ?", like, like, like)
		}

		var total int64
		if err := query.Count(&total).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch full text items"})
			return
		}

		orderBy := map[string]string{
			"next_attempt_at": "feed_items.next_full_text_attempt_at ASC",
			"last_attempt_at": "feed_items.last_full_text_attempt_at DESC",
			"published_at":    "feed_items.published_at DESC",
		}[sort]
		if orderBy == "" {
			orderBy = "feed_items.published_at DESC"
		}

		var items []model.FeedItem
		if err := query.Preload("FeedSource").Order(orderBy).Offset(offset).Limit(limit).Find(&items).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch full text items"})
			return
		}

		rows := make([]adminFeedFullTextItemRow, 0, len(items))
		for _, item := range items {
			row := adminFeedFullTextItemRow{
				ID:                    item.ID,
				Title:                 item.Title,
				Link:                  item.Link,
				FullTextStatus:        item.FullTextStatus,
				FullTextAttemptCount:  item.FullTextAttemptCount,
				FullTextErrorCode:     item.FullTextErrorCode,
				FullTextError:         item.FullTextError,
				LastFullTextAttemptAt: item.LastFullTextAttemptAt,
				NextFullTextAttemptAt: item.NextFullTextAttemptAt,
				PublishedAt:           item.PublishedAt,
				ReaderSource:          item.ReaderSource,
				ReaderQualityScore:    item.ReaderQualityScore,
				ReaderQualityFlags:    item.ReaderQualityFlags,
				ReaderVersion:         item.ReaderVersion,
			}
			if item.FeedSource != nil {
				row.SourceID = item.FeedSource.ID
				row.SourceTitle = item.FeedSource.Title
			}
			rows = append(rows, row)
		}

		c.JSON(http.StatusOK, gin.H{
			"data": rows,
			"meta": gin.H{
				"total": total,
				"page":  page,
				"limit": limit,
			},
		})
	}
}

func RetryAdminFeedFullTextItem(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		itemID := c.Param("item_id")
		var item model.FeedItem
		if err := db.Preload("FeedSource").First(&item, "id = ?", itemID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "Item not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load item"})
			return
		}
		if item.FeedSource == nil || !service.IsFeedItemEligibleForFullText(*item.FeedSource, item) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Only enabled external blog RSS items can be retried"})
			return
		}
		if item.FullTextStatus != service.FullTextStatusFailed {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Only failed full text items can be retried manually"})
			return
		}
		if err := db.Model(&model.FeedItem{}).Where("id = ?", item.ID).Updates(map[string]any{
			"full_text_status":          service.FullTextStatusPending,
			"next_full_text_attempt_at": nil,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retry full text item"})
			return
		}
		service.RequestFullTextWorkerRun()
		c.JSON(http.StatusOK, gin.H{
			"id":               item.ID,
			"full_text_status": service.FullTextStatusPending,
		})
	}
}
