package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/service"
)

type adminFeedFullTextSourceSettingsInput struct {
	FullTextEnabled *bool `json:"full_text_enabled"`
}

type adminFeedFullTextSettings = service.FeedFullTextSettings

type adminFeedFullTextSettingsInput struct {
	AutoSyncEnabled        *bool `json:"auto_sync_enabled"`
	AutoSyncIntervalMinute *int  `json:"auto_sync_interval_minutes"`
	ReaderCrawlEnabled     *bool `json:"reader_crawl_enabled"`
	ReaderCrawlDays        *int  `json:"reader_crawl_days"`
	ReaderCrawlBatchSize   *int  `json:"reader_crawl_batch_size"`
}

func defaultAdminFeedFullTextSettings() adminFeedFullTextSettings {
	return service.DefaultFeedFullTextSettings()
}

func loadAdminFeedFullTextSettings(db *gorm.DB) (adminFeedFullTextSettings, error) {
	return service.LoadFeedFullTextSettings(db)
}

func saveAdminFeedFullTextSettings(db *gorm.DB, settings adminFeedFullTextSettings) error {
	return service.SaveFeedFullTextSettings(db, settings)
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
		if input.AutoSyncEnabled == nil && input.AutoSyncIntervalMinute == nil && input.ReaderCrawlEnabled == nil && input.ReaderCrawlDays == nil && input.ReaderCrawlBatchSize == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_settings_payload"})
			return
		}
		settings, err := loadAdminFeedFullTextSettings(db)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_load_failed"})
			return
		}
		if input.AutoSyncEnabled != nil {
			settings.AutoSyncEnabled = *input.AutoSyncEnabled
		}
		if input.AutoSyncIntervalMinute != nil {
			settings.AutoSyncIntervalMinute = *input.AutoSyncIntervalMinute
		}
		if input.ReaderCrawlEnabled != nil {
			settings.ReaderCrawlEnabled = *input.ReaderCrawlEnabled
		}
		if input.ReaderCrawlDays != nil {
			settings.ReaderCrawlDays = *input.ReaderCrawlDays
		}
		if input.ReaderCrawlBatchSize != nil {
			settings.ReaderCrawlBatchSize = *input.ReaderCrawlBatchSize
		}
		if settings.AutoSyncIntervalMinute < service.FeedFullTextSyncIntervalMin || settings.AutoSyncIntervalMinute > service.FeedFullTextSyncIntervalMax ||
			settings.ReaderCrawlDays < service.FeedReaderCrawlDaysMin || settings.ReaderCrawlDays > service.FeedReaderCrawlDaysMax ||
			settings.ReaderCrawlBatchSize < service.FeedReaderCrawlBatchMin || settings.ReaderCrawlBatchSize > service.FeedReaderCrawlBatchMax {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_settings_payload"})
			return
		}

		if err := saveAdminFeedFullTextSettings(db, settings); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_save_failed"})
			return
		}
		c.JSON(http.StatusOK, settings)
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
