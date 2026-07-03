package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	feedmodule "atoman/internal/modules/feed"
	"atoman/internal/service"
	"atoman/internal/storage"
)

func SetupAdminRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	router.GET("/api/v1/site/access", GetPublicSiteAccessHandler(db))
	router.GET("/api/v1/settings/public/site-access", GetPublicSiteAccessHandler(db))

	settings := router.Group("/api/v1/settings")
	settings.Use(middleware.AuthMiddleware())
	settings.Use(middleware.AdminMiddleware(db))
	{
		settings.GET("/site-access", GetSiteAccessHandler(db))
		settings.PUT("/site-access", UpdateSiteAccessHandler(db))
	}

	admin := router.Group("/api/v1/admin")
	admin.Use(middleware.AuthMiddleware())
	admin.Use(middleware.AdminMiddleware(db))
	{
		admin.PUT("/site-access", UpdateLegacySiteAccessHandler(db))

		feedFullText := admin.Group("/feed/fulltext")
		{
			feedFullText.GET("/settings", GetAdminFeedFullTextSettings(db))
			feedFullText.PUT("/settings", UpdateAdminFeedFullTextSettings(db))
			feedFullText.GET("/health", GetAdminFeedFullTextHealth(db))
			feedFullText.GET("/sources", GetAdminFeedFullTextSources(db))
			feedFullText.POST("/sources", CreateAdminFeedSource(db))
			feedFullText.PUT("/sources/:source_id", UpdateAdminFeedSource(db))
			feedFullText.POST("/sources/:source_id/sync", SyncAdminFeedSource(db))
			feedFullText.GET("/items", GetAdminFeedFullTextItems(db))
			feedFullText.PUT("/sources/:source_id/settings", UpdateAdminFeedFullTextSourceSettings(db))
			feedFullText.POST("/items/:item_id/retry", RetryAdminFeedFullTextItem(db))
		}

		admin.GET("/feed/sources", AdminListFeedSources(db))
		admin.PATCH("/feed/sources/:id", AdminUpdateFeedSourceRow(db))
		admin.DELETE("/feed/sources/:id", AdminDeleteFeedSourceRow(db))

		reviews := admin.Group("/reviews")
		{
			reviews.GET("/songs", GetPendingSongsHandler(db))
			reviews.POST("/songs/:id/approve", ApproveSongHandler(db, s3Client))
			reviews.POST("/songs/:id/reject", RejectSongHandler(db, s3Client))

			reviews.GET("/song-corrections", GetPendingSongCorrectionsHandler(db))
			reviews.POST("/song-corrections/:id/approve", ApproveSongCorrectionHandler(db))
			reviews.POST("/song-corrections/:id/reject", RejectSongCorrectionHandler(db))

			reviews.GET("/albums", GetPendingAlbumsHandler(db))
			reviews.POST("/albums/:id/approve", ApproveAlbumHandler(db, s3Client))
			reviews.POST("/albums/:id/reject", RejectAlbumHandler(db, s3Client))

			reviews.GET("/album-corrections", GetPendingAlbumCorrectionsHandler(db))
			reviews.POST("/album-corrections/:id/approve", ApproveAlbumCorrectionHandler(db))
			reviews.POST("/album-corrections/:id/reject", RejectAlbumCorrectionHandler(db, s3Client))

			reviews.GET("/artist-corrections", GetPendingArtistCorrectionsHandler(db))
			reviews.POST("/artist-corrections/:id/approve", ApproveArtistCorrectionHandler(db))
			reviews.POST("/artist-corrections/:id/reject", RejectArtistCorrectionHandler(db))
		}
	}
}

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

type adminFeedFullTextSourceSettingsInput struct {
	FullTextEnabled *bool `json:"full_text_enabled"`
}

const adminFeedFullTextSettingsKey = "feed.fulltext.settings"

type adminFeedFullTextSettings struct {
	AutoSyncEnabled        bool `json:"auto_sync_enabled"`
	AutoSyncIntervalMinute int  `json:"auto_sync_interval_minutes"`
}

type adminFeedFullTextSettingsInput struct {
	AutoSyncEnabled        *bool `json:"auto_sync_enabled"`
	AutoSyncIntervalMinute *int  `json:"auto_sync_interval_minutes"`
}

type adminFeedSourceListRow struct {
	ID            uuid.UUID  `json:"id"`
	Title         string     `json:"title"`
	Provider      string     `json:"provider"`
	SourceType    string     `json:"source_type"`
	HealthStatus  string     `json:"health_status"`
	LastFetchedAt *time.Time `json:"last_fetched_at"`
	Hidden        bool       `json:"hidden"`
	RssURL        string     `json:"rss_url"`
	SiteURL       string     `json:"site_url"`
	CanonicalURL  string     `json:"canonical_url"`
	BookmarkCount int64      `json:"bookmark_count"`
	ReadCount     int64      `json:"read_count"`
	RecentEvents  []struct {
		EventType string    `json:"event_type"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"recent_events"`
}

type adminFeedSourceUpdateInput struct {
	Title  *string `json:"title"`
	Hidden *bool   `json:"hidden"`
}

type adminFeedSourceInput struct {
	RssURL string `json:"rss_url"`
	Title  string `json:"title"`
}

func normalizeExternalRSSURL(db *gorm.DB, rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", errors.New("rss_url is required")
	}
	if _, err := feedmodule.ResolveInternalRSSURL(db, trimmed); err == nil {
		return "", errors.New("Internal RSS sources are managed separately")
	}

	u, err := url.ParseRequestURI(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("rss_url must be an absolute http/https URL")
	}

	return trimmed, nil
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

func defaultAdminFeedFullTextSettings() adminFeedFullTextSettings {
	return adminFeedFullTextSettings{
		AutoSyncEnabled:        service.FullTextWorkerEnabledDefault,
		AutoSyncIntervalMinute: 2,
	}
}

func loadAdminFeedFullTextSettings(db *gorm.DB) (adminFeedFullTextSettings, error) {
	settings := defaultAdminFeedFullTextSettings()

	var stored model.SiteSetting
	if err := db.First(&stored, "key = ?", adminFeedFullTextSettingsKey).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return settings, nil
		}
		return settings, err
	}

	var input adminFeedFullTextSettingsInput
	if err := json.Unmarshal([]byte(stored.Value), &input); err != nil {
		return settings, err
	}
	if input.AutoSyncEnabled != nil {
		settings.AutoSyncEnabled = *input.AutoSyncEnabled
	}
	if input.AutoSyncIntervalMinute != nil {
		settings.AutoSyncIntervalMinute = *input.AutoSyncIntervalMinute
	}
	return settings, nil
}

func saveAdminFeedFullTextSettings(db *gorm.DB, settings adminFeedFullTextSettings) error {
	value, err := json.Marshal(settings)
	if err != nil {
		return err
	}

	setting := model.SiteSetting{
		Key:         adminFeedFullTextSettingsKey,
		Value:       string(value),
		Description: "Feed full text global settings",
	}
	return db.Save(&setting).Error
}

func GetAdminFeedFullTextSettings(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		settings, err := loadAdminFeedFullTextSettings(db)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_load_failed"})
			return
		}
		c.JSON(http.StatusOK, settings)
	}
}

func UpdateAdminFeedFullTextSettings(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input adminFeedFullTextSettingsInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_settings_payload"})
			return
		}
		if input.AutoSyncEnabled == nil || input.AutoSyncIntervalMinute == nil || *input.AutoSyncIntervalMinute <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_settings_payload"})
			return
		}

		settings := adminFeedFullTextSettings{
			AutoSyncEnabled:        *input.AutoSyncEnabled,
			AutoSyncIntervalMinute: *input.AutoSyncIntervalMinute,
		}
		if err := saveAdminFeedFullTextSettings(db, settings); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_save_failed"})
			return
		}
		c.JSON(http.StatusOK, settings)
	}
}

func AdminListFeedSources(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var sources []model.FeedSource
		if err := db.Order("updated_at DESC").Find(&sources).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load feed sources"})
			return
		}

		items := make([]adminFeedSourceListRow, 0, len(sources))
		for _, source := range sources {
			var bookmarkCount int64
			_ = db.Model(&model.Subscription{}).Where("feed_source_id = ?", source.ID).Count(&bookmarkCount).Error

			var readCount int64
			_ = db.Model(&model.SourceReadEvent{}).Where("source_id = ?", source.ID.String()).Count(&readCount).Error

			var recentSourceEvents []model.SourceReadEvent
			_ = db.Where("source_id = ?", source.ID.String()).Order("created_at DESC").Limit(5).Find(&recentSourceEvents).Error
			recentEvents := make([]struct {
				EventType string    `json:"event_type"`
				CreatedAt time.Time `json:"created_at"`
			}, 0, len(recentSourceEvents))
			for _, event := range recentSourceEvents {
				recentEvents = append(recentEvents, struct {
					EventType string    `json:"event_type"`
					CreatedAt time.Time `json:"created_at"`
				}{
					EventType: event.EventType,
					CreatedAt: event.CreatedAt,
				})
			}

			items = append(items, adminFeedSourceListRow{
				ID:            source.ID,
				Title:         source.Title,
				Provider:      source.Provider,
				SourceType:    source.SourceType,
				HealthStatus:  source.HealthStatus,
				LastFetchedAt: source.LastFetchedAt,
				Hidden:        source.Hidden,
				RssURL:        source.RssURL,
				SiteURL:       source.SiteURL,
				CanonicalURL:  source.CanonicalURL,
				BookmarkCount: bookmarkCount,
				ReadCount:     readCount,
				RecentEvents:  recentEvents,
			})
		}

		c.JSON(http.StatusOK, gin.H{"items": items})
	}
}

func AdminUpdateFeedSourceRow(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.Param("id"))
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "source id is required"})
			return
		}

		var input adminFeedSourceUpdateInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}

		updates := map[string]any{}
		if input.Title != nil {
			trimmedTitle := strings.TrimSpace(*input.Title)
			if trimmedTitle == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "title must not be blank"})
				return
			}
			updates["title"] = trimmedTitle
		}
		if input.Hidden != nil {
			updates["hidden"] = *input.Hidden
		}
		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
			return
		}

		result := db.Model(&model.FeedSource{}).Where("id = ?", id).Updates(updates)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update feed source"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "feed source not found"})
			return
		}

		var source model.FeedSource
		if err := db.First(&source, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reload feed source"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"item": source})
	}
}

func AdminDeleteFeedSourceRow(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.Param("id"))
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "source id is required"})
			return
		}

		if err := db.Transaction(func(tx *gorm.DB) error {
			var source model.FeedSource
			if err := tx.First(&source, "id = ?", id).Error; err != nil {
				return err
			}
			if err := tx.Where("feed_source_id = ?", source.ID).Delete(&model.Subscription{}).Error; err != nil {
				return err
			}
			var itemIDs []uuid.UUID
			if err := tx.Model(&model.FeedItem{}).Where("feed_source_id = ?", source.ID).Pluck("id", &itemIDs).Error; err != nil {
				return err
			}
			if len(itemIDs) > 0 {
				if err := tx.Where("feed_item_id IN ?", itemIDs).Delete(&model.FeedItemRead{}).Error; err != nil {
					return err
				}
				if err := tx.Where("feed_item_id IN ?", itemIDs).Delete(&model.FeedItemStar{}).Error; err != nil {
					return err
				}
				if err := tx.Where("feed_item_id IN ?", itemIDs).Delete(&model.ReadingListItem{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("feed_source_id = ?", source.ID).Delete(&model.FeedItem{}).Error; err != nil {
				return err
			}
			return tx.Delete(&source).Error
		}); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "feed source not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete feed source"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
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

func CreateAdminFeedSource(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input adminFeedSourceInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid source payload"})
			return
		}

		rssURL, err := normalizeExternalRSSURL(db, input.RssURL)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		source, err := feedmodule.FindOrCreateFeedSource(db, "external_rss", nil, rssURL, input.Title, "")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create feed source"})
			return
		}

		title := strings.TrimSpace(input.Title)
		if title != "" && title != source.Title {
			if err := db.Model(&model.FeedSource{}).Where("id = ?", source.ID).Update("title", title).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create feed source"})
				return
			}
			source.Title = title
		}

		c.JSON(http.StatusCreated, gin.H{
			"id":                source.ID,
			"title":             source.Title,
			"rss_url":           source.RssURL,
			"source_type":       source.SourceType,
			"full_text_enabled": source.FullTextEnabled,
		})
	}
}

func UpdateAdminFeedSource(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input adminFeedSourceInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid source payload"})
			return
		}

		var source model.FeedSource
		if err := db.First(&source, "id = ?", c.Param("source_id")).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "Source not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load source"})
			return
		}
		if source.SourceType != "external_rss" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Only external RSS sources can be updated here"})
			return
		}

		rssURL, err := normalizeExternalRSSURL(db, input.RssURL)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{
			"rss_url": rssURL,
			"hash":    feedmodule.BuildFeedSourceHash("external_rss", nil, rssURL),
			"title":   strings.TrimSpace(input.Title),
		}
		if strings.TrimSpace(input.Title) == "" {
			delete(updates, "title")
		}

		if err := db.Model(&model.FeedSource{}).Where("id = ?", source.ID).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update feed source"})
			return
		}
		if err := db.First(&source, "id = ?", source.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load source"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"id":                source.ID,
			"title":             source.Title,
			"rss_url":           source.RssURL,
			"source_type":       source.SourceType,
			"full_text_enabled": source.FullTextEnabled,
		})
	}
}

func SyncAdminFeedSource(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var source model.FeedSource
		if err := db.First(&source, "id = ?", c.Param("source_id")).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "Source not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load source"})
			return
		}
		if source.SourceType != "external_rss" || strings.Contains(source.RssURL, "/api/v1/feed/rss/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Only external blog RSS sources can be synced manually"})
			return
		}

		go service.SyncSingleRSS(db, source)

		c.JSON(http.StatusOK, gin.H{
			"id":      source.ID,
			"message": "sync_started",
		})
	}
}

func UpdateAdminFeedFullTextSourceSettings(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input adminFeedFullTextSourceSettingsInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid settings payload"})
			return
		}
		if input.FullTextEnabled == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "full_text_enabled is required"})
			return
		}

		sourceID := c.Param("source_id")
		var source model.FeedSource
		if err := db.First(&source, "id = ?", sourceID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "Source not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load source"})
			return
		}
		if source.SourceType != "external_rss" || strings.Contains(source.RssURL, "/api/v1/feed/rss/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Only external blog RSS sources support full text settings"})
			return
		}
		if err := db.Model(&model.FeedSource{}).Where("id = ?", source.ID).Update("full_text_enabled", *input.FullTextEnabled).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update source settings"})
			return
		}
		if *input.FullTextEnabled {
			if err := db.Model(&model.FeedItem{}).
				Where("feed_source_id = ? AND full_text_status = ? AND link LIKE ?", source.ID, service.FullTextStatusDisabled, "http%").
				Where("COALESCE(enclosure_url, '') = ''").
				Where("COALESCE(enclosure_type, '') NOT LIKE ?", "audio/%").
				Where("COALESCE(enclosure_type, '') NOT LIKE ?", "video/%").
				Where("COALESCE(duration, '') = ''").
				Updates(map[string]any{
					"full_text_status":          service.FullTextStatusPending,
					"next_full_text_attempt_at": nil,
				}).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to requeue disabled items"})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"id":                source.ID,
			"full_text_enabled": *input.FullTextEnabled,
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

func GetPublicSiteAccessHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		matrix, err := service.NewSiteAccessService(db).PublicMatrix()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_load_failed"})
			return
		}
		c.JSON(http.StatusOK, matrix)
	}
}

// GetSiteAccessHandler godoc
// @Summary 获取站点模块访问设置
// @Description 返回后台模块开关与结构化模块设置，包括 feed/blog/forum 的设置矩阵。
// @Tags settings
// @Produce json
// @Success 200 {object} service.SiteAccessMatrix
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/settings/site-access [get]
func GetSiteAccessHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		matrix, err := service.NewSiteAccessService(db).Load()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_load_failed"})
			return
		}
		c.JSON(http.StatusOK, matrix)
	}
}

// UpdateSiteAccessHandler godoc
// @Summary 更新站点模块访问设置
// @Description 保存后台模块开关与结构化模块设置，包括 feed/blog/forum 的设置矩阵。
// @Tags settings
// @Accept json
// @Produce json
// @Param input body service.SiteAccessMatrixInput true "站点访问设置输入"
// @Success 200 {object} service.SiteAccessMatrix
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/settings/site-access [put]
func UpdateSiteAccessHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var payload service.SiteAccessMatrixInput
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_site_access_payload"})
			return
		}

		svc := service.NewSiteAccessService(db)
		if err := svc.SaveInput(payload); err != nil {
			if errors.Is(err, service.ErrInvalidSiteAccessPayload) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_site_access_payload"})
				return
			}
			if errors.Is(err, service.ErrSiteAccessConflict) {
				c.JSON(http.StatusConflict, gin.H{"error": "site_access_conflict"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_save_failed"})
			return
		}

		matrix, err := svc.Load()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_save_failed"})
			return
		}
		c.JSON(http.StatusOK, matrix)
	}
}

func UpdateLegacySiteAccessHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_site_access_payload"})
			return
		}

		svc := service.NewSiteAccessService(db)
		if err := svc.SaveLegacyPayload(body); err != nil {
			if errors.Is(err, service.ErrInvalidSiteAccessPayload) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_site_access_payload"})
				return
			}
			if errors.Is(err, service.ErrSiteAccessConflict) {
				c.JSON(http.StatusConflict, gin.H{"error": "site_access_conflict"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_save_failed"})
			return
		}

		matrix, err := svc.Load()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_save_failed"})
			return
		}
		c.JSON(http.StatusOK, matrix)
	}
}

func canUploadToS3(s3Client *s3.S3) bool {
	return s3Client != nil && os.Getenv("S3_BUCKET") != "" && os.Getenv("S3_URL_PREFIX") != ""
}

// GetPendingSongsHandler godoc
// @Summary 获取待审核歌曲列表
// @Description 返回所有待审核歌曲及其关联用户、专辑和艺人信息。
// @Tags admin
// @Produce json
// @Success 200 {array} model.Song
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/songs [get]
func GetPendingSongsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var songs []model.Song
		if err := db.Where("status = ?", "pending").
			Preload("User").
			Preload("Album").
			Preload("Album.Artists").
			Preload("Artists").
			Order("created_at desc").
			Find(&songs).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch pending songs"})
			return
		}
		c.JSON(http.StatusOK, songs)
	}
}

// ApproveSongHandler godoc
// @Summary 审核通过歌曲
// @Description 将待审核歌曲标记为 approved，并在可用时把本地文件迁移到 S3。
// @Tags admin
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/songs/{id}/approve [post]
func ApproveSongHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var song model.Song
		if err := db.First(&song, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Song not found"})
			return
		}

		if canUploadToS3(s3Client) && song.AudioSource == "local" && song.AudioURL != "" {
			localPath := storage.GetLocalPathFromURL(song.AudioURL)
			if localPath != "" {
				s3URL, err := storage.UploadLocalFileToS3(s3Client, localPath)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload audio to S3"})
					return
				}
				song.AudioURL = s3URL
				song.AudioSource = "s3"
				storage.DeleteLocalFile(localPath)
			}
		}

		if canUploadToS3(s3Client) && song.CoverSource == "local" && song.CoverURL != "" {
			localPath := storage.GetLocalPathFromURL(song.CoverURL)
			if localPath != "" {
				s3URL, err := storage.UploadLocalFileToS3(s3Client, localPath)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload cover to S3"})
					return
				}
				song.CoverURL = s3URL
				song.CoverSource = "s3"
				storage.DeleteLocalFile(localPath)
			}
		}

		song.Status = "approved"

		if err := db.Save(&song).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to approve song"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Song approved"})
	}
}

// RejectSongHandler godoc
// @Summary 驳回歌曲
// @Description 删除待审核歌曲及其关联本地或对象存储文件。
// @Tags admin
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/songs/{id}/reject [post]
func RejectSongHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var song model.Song
		if err := db.Preload("Album").First(&song, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Song not found"})
			return
		}

		if err := storage.DeleteSongAndS3Objects(db, s3Client, &song); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete song and associated files"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Song rejected and deleted"})
	}
}

// GetPendingSongCorrectionsHandler godoc
// @Summary 获取待审核歌曲纠错列表
// @Description 返回所有待审核歌曲纠错及其关联歌曲、提交用户信息。
// @Tags admin
// @Produce json
// @Success 200 {array} model.SongCorrection
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/song-corrections [get]
func GetPendingSongCorrectionsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var corrections []model.SongCorrection
		if err := db.Where("status = ?", "pending").
			Preload("User").
			Preload("Song").
			Order("created_at desc").
			Find(&corrections).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch pending song corrections"})
			return
		}
		c.JSON(http.StatusOK, corrections)
	}
}

// ApproveSongCorrectionHandler godoc
// @Summary 审核通过歌曲纠错
// @Description 将歌曲纠错标记为 approved，并把支持的字段改动应用到歌曲实体。
// @Tags admin
// @Produce json
// @Param id path string true "纠错 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/song-corrections/{id}/approve [post]
func ApproveSongCorrectionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		adminIDVal, _ := c.Get("user_id")
		adminID := adminIDVal.(uuid.UUID)
		now := time.Now()

		var correction model.SongCorrection
		if err := db.Preload("Song").First(&correction, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Correction not found"})
			return
		}

		tx := db.Begin()

		if err := tx.Model(&model.SongCorrection{}).Where("id = ?", id).Updates(map[string]interface{}{
			"status":      "approved",
			"approved_by": adminID,
			"approved_at": now,
		}).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to approve correction"})
			return
		}

		song := correction.Song
		updated := false

		switch correction.FieldName {
		case "title":
			song.Title = correction.CorrectedValue
			updated = true
		case "lyrics":
			song.Lyrics = correction.CorrectedValue
			updated = true
		}

		if updated {
			if err := tx.Save(&song).Error; err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply correction"})
				return
			}
		}

		tx.Commit()
		c.JSON(http.StatusOK, gin.H{"message": "Song correction approved and applied"})
	}
}

// RejectSongCorrectionHandler godoc
// @Summary 驳回歌曲纠错
// @Description 将歌曲纠错标记为 rejected。
// @Tags admin
// @Produce json
// @Param id path string true "纠错 UUID"
// @Success 200 {object} MessageResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/song-corrections/{id}/reject [post]
func RejectSongCorrectionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		adminIDVal, _ := c.Get("user_id")
		adminID := adminIDVal.(uuid.UUID)
		now := time.Now()

		if err := db.Model(&model.SongCorrection{}).Where("id = ?", id).Updates(map[string]interface{}{
			"status":      "rejected",
			"rejected_by": adminID,
			"rejected_at": now,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reject correction"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Song correction rejected"})
	}
}

// GetPendingAlbumsHandler godoc
// @Summary 获取待审核专辑列表
// @Description 返回所有待审核专辑及其关联艺人、用户和歌曲信息。
// @Tags admin
// @Produce json
// @Success 200 {array} model.Album
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/albums [get]
func GetPendingAlbumsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var albums []model.Album
		if err := db.Where("status = ?", "pending").
			Preload("Artists").
			Preload("User").
			Preload("Songs").
			Order("created_at desc").
			Find(&albums).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch pending albums"})
			return
		}
		c.JSON(http.StatusOK, albums)
	}
}

// ApproveAlbumHandler godoc
// @Summary 审核通过专辑
// @Description 将待审核专辑及其附属歌曲标记为 approved，并在可用时迁移本地文件到 S3。
// @Tags admin
// @Produce json
// @Param id path string true "专辑 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/albums/{id}/approve [post]
func ApproveAlbumHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var album model.Album
		if err := db.Preload("Songs").First(&album, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Album not found"})
			return
		}

		// Upload album cover to S3 if local
		if canUploadToS3(s3Client) && album.CoverSource == "local" && album.CoverURL != "" {
			localPath := storage.GetLocalPathFromURL(album.CoverURL)
			if localPath != "" {
				s3URL, err := storage.UploadLocalFileToS3(s3Client, localPath)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload cover to S3"})
					return
				}
				album.CoverURL = s3URL
				album.CoverSource = "s3"
				storage.DeleteLocalFile(localPath)
			}
		}

		// Upload all songs' local files to S3
		for i := range album.Songs {
			song := &album.Songs[i]
			if canUploadToS3(s3Client) && song.AudioSource == "local" && song.AudioURL != "" {
				localPath := storage.GetLocalPathFromURL(song.AudioURL)
				if localPath != "" {
					s3URL, err := storage.UploadLocalFileToS3(s3Client, localPath)
					if err != nil {
						log.Printf("Failed to upload song audio to S3: %v", err)
						continue
					}
					song.AudioURL = s3URL
					song.AudioSource = "s3"
					storage.DeleteLocalFile(localPath)
				}
			}
			if canUploadToS3(s3Client) && song.CoverSource == "local" && song.CoverURL != "" {
				localPath := storage.GetLocalPathFromURL(song.CoverURL)
				if localPath != "" {
					s3URL, err := storage.UploadLocalFileToS3(s3Client, localPath)
					if err != nil {
						log.Printf("Failed to upload song cover to S3: %v", err)
						continue
					}
					song.CoverURL = s3URL
					song.CoverSource = "s3"
					storage.DeleteLocalFile(localPath)
				}
			}
			song.Status = "approved"
			db.Save(song)
		}

		album.Status = "approved"

		if err := db.Save(&album).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to approve album"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Album approved"})
	}
}

// RejectAlbumHandler godoc
// @Summary 驳回专辑
// @Description 删除待审核专辑及其关联存储对象。
// @Tags admin
// @Produce json
// @Param id path string true "专辑 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/albums/{id}/reject [post]
func RejectAlbumHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var album model.Album
		if err := db.First(&album, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Album not found"})
			return
		}

		if err := storage.DeleteAlbumAndS3Objects(db, s3Client, &album); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete album and associated files"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Album rejected and deleted"})
	}
}

// GetPendingAlbumCorrectionsHandler godoc
// @Summary 获取待审核专辑纠错列表
// @Description 返回所有待审核专辑纠错及其关联专辑信息。
// @Tags admin
// @Produce json
// @Success 200 {array} model.AlbumCorrection
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/album-corrections [get]
func GetPendingAlbumCorrectionsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var corrections []model.AlbumCorrection
		if err := db.Where("status = ?", "pending").
			Preload("User").
			Preload("Album").
			Preload("Album.Artists").
			Order("created_at desc").
			Find(&corrections).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch pending album corrections"})
			return
		}
		c.JSON(http.StatusOK, corrections)
	}
}

// ApproveAlbumCorrectionHandler godoc
// @Summary 审核通过专辑纠错
// @Description 将专辑纠错标记为 approved，并把改动应用到专辑实体。
// @Tags admin
// @Produce json
// @Param id path string true "纠错 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/album-corrections/{id}/approve [post]
func ApproveAlbumCorrectionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		adminIDVal, _ := c.Get("user_id")
		adminID := adminIDVal.(uuid.UUID)
		now := time.Now()

		var correction model.AlbumCorrection
		if err := db.Preload("Album").Preload("Album.Artists").First(&correction, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Correction not found"})
			return
		}

		tx := db.Begin()

		if err := tx.Model(&model.AlbumCorrection{}).Where("id = ?", id).Updates(map[string]interface{}{
			"status":      "approved",
			"approved_by": adminID,
			"approved_at": now,
		}).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to approve correction"})
			return
		}

		var album model.Album
		if err := tx.First(&album, "id = ?", correction.AlbumID).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusNotFound, gin.H{"error": "Album not found"})
			return
		}

		if correction.CorrectedTitle != "" {
			album.Title = correction.CorrectedTitle
		}
		if correction.CorrectedCoverURL != "" {
			album.CoverURL = correction.CorrectedCoverURL
			album.CoverSource = correction.CorrectedCoverSource
		}
		if correction.CorrectedReleaseDate != nil {
			album.ReleaseDate = *correction.CorrectedReleaseDate
		}

		if err := tx.Save(&album).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply album correction"})
			return
		}

		tx.Commit()
		c.JSON(http.StatusOK, gin.H{"message": "Album correction approved and applied"})
	}
}

// RejectAlbumCorrectionHandler godoc
// @Summary 驳回专辑纠错
// @Description 将专辑纠错标记为 rejected。
// @Tags admin
// @Produce json
// @Param id path string true "纠错 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/album-corrections/{id}/reject [post]
func RejectAlbumCorrectionHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		adminIDVal, _ := c.Get("user_id")
		adminID := adminIDVal.(uuid.UUID)
		now := time.Now()

		var correction model.AlbumCorrection
		if err := db.First(&correction, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Correction not found"})
			return
		}

		if err := db.Model(&model.AlbumCorrection{}).Where("id = ?", id).Updates(map[string]interface{}{
			"status":      "rejected",
			"rejected_by": adminID,
			"rejected_at": now,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reject correction"})
			return
		}

		if correction.CorrectedCoverURL != "" && correction.CorrectedCoverSource == "s3" {
			log.Printf("Note: Should delete S3 object for rejected cover: %s", correction.CorrectedCoverURL)
		}

		c.JSON(http.StatusOK, gin.H{"message": "Album correction rejected"})
	}
}

// GetPendingArtistCorrectionsHandler godoc
// @Summary 获取待审核艺人纠错列表
// @Description 返回所有待审核艺人纠错及其关联艺人、提交用户信息。
// @Tags admin
// @Produce json
// @Success 200 {array} model.ArtistCorrection
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/artist-corrections [get]
func GetPendingArtistCorrectionsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var corrections []model.ArtistCorrection
		if err := db.Where("status = ?", "pending").
			Preload("Artist").
			Preload("User").
			Order("created_at asc").
			Find(&corrections).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch pending artist corrections"})
			return
		}
		c.JSON(http.StatusOK, corrections)
	}
}

// ApproveArtistCorrectionHandler godoc
// @Summary 审核通过艺人纠错
// @Description 将艺人纠错标记为 approved。
// @Tags admin
// @Produce json
// @Param id path string true "纠错 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/artist-corrections/{id}/approve [post]
func ApproveArtistCorrectionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		adminIDVal, _ := c.Get("user_id")
		adminID := adminIDVal.(uuid.UUID)
		now := time.Now()

		var correction model.ArtistCorrection
		if err := db.First(&correction, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Correction not found"})
			return
		}

		if err := db.Model(&model.ArtistCorrection{}).Where("id = ?", id).Updates(map[string]interface{}{
			"status":      "approved",
			"approved_by": adminID,
			"approved_at": now,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to approve correction"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Artist correction approved"})
	}
}

// RejectArtistCorrectionHandler godoc
// @Summary 驳回艺人纠错
// @Description 将艺人纠错标记为 rejected。
// @Tags admin
// @Produce json
// @Param id path string true "纠错 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/artist-corrections/{id}/reject [post]
func RejectArtistCorrectionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var correction model.ArtistCorrection
		if err := db.First(&correction, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Correction not found"})
			return
		}

		if err := db.Model(&model.ArtistCorrection{}).Where("id = ?", id).Update("status", "rejected").Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reject correction"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Artist correction rejected"})
	}
}
