package handlers

import (
	"encoding/json"
	"errors"
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

const adminFeedFullTextSettingsKey = "feed.fulltext.settings"

type adminFeedFullTextSettings struct {
	AutoSyncEnabled        bool `json:"auto_sync_enabled"`
	AutoSyncIntervalMinute int  `json:"auto_sync_interval_minutes"`
}

type adminFeedFullTextSettingsInput struct {
	AutoSyncEnabled        *bool `json:"auto_sync_enabled"`
	AutoSyncIntervalMinute *int  `json:"auto_sync_interval_minutes"`
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
