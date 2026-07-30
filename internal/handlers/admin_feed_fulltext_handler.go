package handlers

import (
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
	ID              uuid.UUID  `json:"id"`
	Title           string     `json:"title"`
	RssURL          string     `json:"rss_url"`
	FullTextEnabled bool       `json:"full_text_enabled"`
	SuccessCount    int64      `json:"success_count"`
	RetryCount      int64      `json:"retry_count"`
	FailedCount     int64      `json:"failed_count"`
	PendingCount    int64      `json:"pending_count"`
	SuccessRate     float64    `json:"success_rate"`
	Status          string     `json:"status"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
	LastFailureAt   *time.Time `json:"last_failure_at"`
	LastErrorCode   string     `json:"last_error_code"`
	LastError       string     `json:"last_error"`
}

type adminFeedFullTextItemRow struct {
	ID                    uuid.UUID  `json:"id"`
	Title                 string     `json:"title"`
	Link                  string     `json:"link"`
	SourceID              uuid.UUID  `json:"source_id"`
	SourceTitle           string     `json:"source_title"`
	FullTextStatus        string     `json:"full_text_status"`
	FullTextAttemptCount  int        `json:"attempt_count"`
	FullTextErrorCode     string     `json:"error_code"`
	FullTextError         string     `json:"error_message"`
	LastFullTextAttemptAt *time.Time `json:"last_attempt_at"`
	NextFullTextAttemptAt *time.Time `json:"next_attempt_at"`
	PublishedAt           time.Time  `json:"published_at"`
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
		var fetchingItems int64
		var retryItems int64
		var successItems int64
		var failedItems int64
		var latestSuccessAt *time.Time
		var latestFailureAt *time.Time
		var oldestPendingItem model.FeedItem

		externalItems := func() *gorm.DB { return adminFullTextBlogItemQuery(db) }
		externalSources := func() *gorm.DB { return adminFullTextBlogSourceQuery(db) }

		externalSources().Where("full_text_enabled = ?", true).Count(&enabledSources)
		externalSources().Where("full_text_enabled = ?", false).Count(&disabledSources)
		externalItems().Where("feed_items.full_text_status = ?", service.FullTextStatusPending).Count(&pendingItems)
		externalItems().Where("feed_items.full_text_status = ?", service.FullTextStatusFetching).Count(&fetchingItems)
		externalItems().Where("feed_items.full_text_status = ?", service.FullTextStatusRetry).Count(&retryItems)
		externalItems().Where("feed_items.full_text_status = ?", service.FullTextStatusSuccess).Count(&successItems)
		externalItems().Where("feed_items.full_text_status = ?", service.FullTextStatusFailed).Count(&failedItems)
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

		payload := gin.H{
			"enabled_sources":  enabledSources,
			"disabled_sources": disabledSources,
			"pending_items":    pendingItems,
			"fetching_items":   fetchingItems,
			"retry_items":      retryItems,
			"success_items":    successItems,
			"failed_items":     failedItems,
			"success_rate":     successRate,
			"enabled":          service.FullTextWorkerEnabledDefault,
			"concurrency":      service.FullTextWorkerConcurrency,
			"timeout_seconds":  int(service.FullTextWorkerTimeout / time.Second),
			"max_attempts":     service.FullTextWorkerMaxAttempts,
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

		var sources []model.FeedSource
		if err := query.Find(&sources).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch full text sources"})
			return
		}

		rows := make([]adminFeedFullTextSourceRow, 0, len(sources))
		for _, source := range sources {
			var pendingCount int64
			var retryCount int64
			var failedCount int64
			var successCount int64
			adminFullTextBlogItemQuery(db).Where("feed_items.feed_source_id = ? AND feed_items.full_text_status = ?", source.ID, service.FullTextStatusPending).Count(&pendingCount)
			adminFullTextBlogItemQuery(db).Where("feed_items.feed_source_id = ? AND feed_items.full_text_status = ?", source.ID, service.FullTextStatusRetry).Count(&retryCount)
			adminFullTextBlogItemQuery(db).Where("feed_items.feed_source_id = ? AND feed_items.full_text_status = ?", source.ID, service.FullTextStatusFailed).Count(&failedCount)
			adminFullTextBlogItemQuery(db).Where("feed_items.feed_source_id = ? AND feed_items.full_text_status = ?", source.ID, service.FullTextStatusSuccess).Count(&successCount)

			completed := successCount + failedCount
			successRate := 0.0
			if completed > 0 {
				successRate = float64(successCount) / float64(completed)
			}
			row := adminFeedFullTextSourceRow{
				ID:              source.ID,
				Title:           source.Title,
				RssURL:          source.RssURL,
				FullTextEnabled: source.FullTextEnabled,
				SuccessCount:    successCount,
				RetryCount:      retryCount,
				FailedCount:     failedCount,
				PendingCount:    pendingCount,
				SuccessRate:     successRate,
				Status:          adminFeedFullTextHealthStatus(source.FullTextEnabled, pendingCount, retryCount, failedCount, successCount),
				LastSuccessAt:   source.FullTextLastSuccessAt,
				LastFailureAt:   source.FullTextLastFailureAt,
				LastErrorCode:   source.FullTextLastErrorCode,
				LastError:       source.FullTextLastError,
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
		c.JSON(http.StatusOK, gin.H{
			"id":               item.ID,
			"full_text_status": service.FullTextStatusPending,
		})
	}
}
