package service

import (
	"encoding/json"
	"fmt"

	"atoman/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	FeedFullTextSettingsKey = "feed.fulltext.settings"

	FeedFullTextSyncIntervalDefault = 5
	FeedFullTextSyncIntervalMin     = 5
	FeedFullTextSyncIntervalMax     = 1440
	FeedReaderCrawlDaysDefault      = 90
	FeedReaderCrawlDaysMin          = 1
	FeedReaderCrawlDaysMax          = 3650
	FeedReaderCrawlBatchDefault     = 100
	FeedReaderCrawlBatchMin         = 1
	FeedReaderCrawlBatchMax         = 100
)

// FeedFullTextSettings controls scheduled feed article extraction and legacy
// reader-content recovery. Environment settings remain the deployment-level
// master switch and resource ceiling.
type FeedFullTextSettings struct {
	AutoSyncEnabled        bool `json:"auto_sync_enabled"`
	AutoSyncIntervalMinute int  `json:"auto_sync_interval_minutes"`
	ReaderCrawlEnabled     bool `json:"reader_crawl_enabled"`
	ReaderCrawlDays        int  `json:"reader_crawl_days"`
	ReaderCrawlBatchSize   int  `json:"reader_crawl_batch_size"`
}

func DefaultFeedFullTextSettings() FeedFullTextSettings {
	return FeedFullTextSettings{
		AutoSyncEnabled:        FullTextWorkerEnabledDefault,
		AutoSyncIntervalMinute: FeedFullTextSyncIntervalDefault,
		ReaderCrawlEnabled:     true,
		ReaderCrawlDays:        FeedReaderCrawlDaysDefault,
		ReaderCrawlBatchSize:   FeedReaderCrawlBatchDefault,
	}
}

func normalizeFeedFullTextSettings(settings FeedFullTextSettings) FeedFullTextSettings {
	defaults := DefaultFeedFullTextSettings()
	if settings.AutoSyncIntervalMinute < FeedFullTextSyncIntervalMin || settings.AutoSyncIntervalMinute > FeedFullTextSyncIntervalMax {
		settings.AutoSyncIntervalMinute = defaults.AutoSyncIntervalMinute
	}
	if settings.ReaderCrawlDays < FeedReaderCrawlDaysMin || settings.ReaderCrawlDays > FeedReaderCrawlDaysMax {
		settings.ReaderCrawlDays = defaults.ReaderCrawlDays
	}
	if settings.ReaderCrawlBatchSize < FeedReaderCrawlBatchMin || settings.ReaderCrawlBatchSize > FeedReaderCrawlBatchMax {
		settings.ReaderCrawlBatchSize = defaults.ReaderCrawlBatchSize
	}
	return settings
}

func EnsureFeedFullTextSettings(db *gorm.DB) error {
	value, err := json.Marshal(DefaultFeedFullTextSettings())
	if err != nil {
		return fmt.Errorf("encode default feed full text settings: %w", err)
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.SiteSetting{
		Key:         FeedFullTextSettingsKey,
		Value:       string(value),
		Description: "Feed full text global settings",
	}).Error; err != nil {
		return fmt.Errorf("ensure feed full text settings: %w", err)
	}
	return nil
}

func LoadFeedFullTextSettings(db *gorm.DB) (FeedFullTextSettings, error) {
	settings := DefaultFeedFullTextSettings()
	var stored model.SiteSetting
	result := db.Where("key = ?", FeedFullTextSettingsKey).Limit(1).Find(&stored)
	if result.Error != nil {
		return FeedFullTextSettings{}, fmt.Errorf("load feed full text settings: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		if err := EnsureFeedFullTextSettings(db); err != nil {
			return FeedFullTextSettings{}, err
		}
		return settings, nil
	}
	if err := json.Unmarshal([]byte(stored.Value), &settings); err != nil {
		return FeedFullTextSettings{}, fmt.Errorf("decode feed full text settings: %w", err)
	}
	return normalizeFeedFullTextSettings(settings), nil
}

func SaveFeedFullTextSettings(db *gorm.DB, settings FeedFullTextSettings) error {
	value, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("encode feed full text settings: %w", err)
	}
	if err := db.Save(&model.SiteSetting{
		Key:         FeedFullTextSettingsKey,
		Value:       string(value),
		Description: "Feed full text global settings",
	}).Error; err != nil {
		return fmt.Errorf("save feed full text settings: %w", err)
	}
	return nil
}
