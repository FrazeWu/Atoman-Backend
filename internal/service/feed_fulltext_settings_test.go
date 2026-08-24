package service

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestLoadFeedFullTextSettingsSeedsMissingDefaults(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.SiteSetting{})

	settings, err := LoadFeedFullTextSettings(db)
	if err != nil {
		t.Fatal(err)
	}
	defaults := DefaultFeedFullTextSettings()
	if settings != defaults {
		t.Fatalf("settings=%+v, want defaults=%+v", settings, defaults)
	}

	var stored model.SiteSetting
	if err := db.First(&stored, "key = ?", FeedFullTextSettingsKey).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Value == "" {
		t.Fatal("expected default feed full text settings to be persisted")
	}
}

func TestLoadFeedFullTextSettingsPreservesDefaultsForLegacyJSON(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.SiteSetting{})
	if err := db.Create(&model.SiteSetting{
		Key:   FeedFullTextSettingsKey,
		Value: `{"auto_sync_enabled":false,"auto_sync_interval_minutes":45}`,
	}).Error; err != nil {
		t.Fatal(err)
	}

	settings, err := LoadFeedFullTextSettings(db)
	if err != nil {
		t.Fatal(err)
	}
	if settings.AutoSyncEnabled || settings.AutoSyncIntervalMinute != 45 {
		t.Fatalf("legacy fields changed: %+v", settings)
	}
	if !settings.ReaderCrawlEnabled || settings.ReaderCrawlDays != FeedReaderCrawlDaysDefault || settings.ReaderCrawlBatchSize != FeedReaderCrawlBatchDefault {
		t.Fatalf("reader crawl defaults missing: %+v", settings)
	}
}

func TestRunConfiguredFullTextCycleRespectsManagedAutoCrawlSetting(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	testdb.Migrate(t, db, &model.SiteSetting{})
	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "configured-reader-crawl",
		RssURL:          "https://example.com/feed.xml",
		FullTextEnabled: false,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := model.FeedItem{
		FeedSourceID:   source.ID,
		GUID:           "configured-reader-candidate",
		Link:           "https://example.com/configured",
		FullTextHTML:   `<article><p>Configured stored article content with enough words to remain useful for reading and indexing.</p></article>`,
		PublishedAt:    now,
		FetchedAt:      now,
		FullTextStatus: FullTextStatusSuccess,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	settings := DefaultFeedFullTextSettings()
	settings.AutoSyncEnabled = false
	settings.ReaderCrawlBatchSize = 10
	cfg := fullTextWorkerConfig{BatchSize: 1, Concurrency: 1}
	runConfiguredFullTextCycle(db, now, cfg, settings)
	if err := db.First(&item, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.ReaderHTML != "" {
		t.Fatal("disabled managed crawl should not update reader content")
	}

	settings.AutoSyncEnabled = true
	runConfiguredFullTextCycle(db, now, cfg, settings)
	if err := db.First(&item, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.ReaderHTML == "" || item.ReaderVersion != ReaderVersionCurrent {
		t.Fatalf("enabled managed crawl did not update reader content: %+v", item)
	}
}

func TestRunFeedReaderCrawlHonorsDateRangeAndPersistsStatus(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	testdb.Migrate(t, db, &model.SiteSetting{})
	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "managed-reader-crawl",
		RssURL:          "https://example.com/feed.xml",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	items := []model.FeedItem{
		{
			FeedSourceID:    source.ID,
			GUID:            "recent-reader-candidate",
			Link:            "https://example.com/recent",
			FeedContentHTML: `<article><p>Recent stored article content with enough words to remain useful for reading and indexing.</p></article>`,
			PublishedAt:     now.Add(-12 * time.Hour),
			FetchedAt:       now,
		},
		{
			FeedSourceID:    source.ID,
			GUID:            "old-reader-candidate",
			Link:            "https://example.com/old",
			FeedContentHTML: `<article><p>Old stored article content with enough words to remain useful for reading and indexing.</p></article>`,
			PublishedAt:     now.Add(-48 * time.Hour),
			FetchedAt:       now,
		},
	}
	for index := range items {
		if err := db.Create(&items[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	settings := DefaultFeedFullTextSettings()
	settings.ReaderCrawlDays = 1
	settings.ReaderCrawlBatchSize = 10
	result, err := RunFeedReaderCrawl(db, settings, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Updated != 1 {
		t.Fatalf("result=%+v", result)
	}
	if err := db.First(&items[0], "id = ?", items[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if items[0].ReaderHTML == "" || items[0].ReaderVersion != ReaderVersionCurrent {
		t.Fatalf("recent item was not rebuilt: %+v", items[0])
	}
	if err := db.First(&items[1], "id = ?", items[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if items[1].ReaderHTML != "" {
		t.Fatalf("old item should remain untouched: %+v", items[1])
	}
	status, err := LoadFeedReaderCrawlStatus(db)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastRunAt.IsZero() || status.Scanned != 1 || status.Updated != 1 {
		t.Fatalf("status=%+v", status)
	}
}
