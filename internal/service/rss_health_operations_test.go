package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRSSFetchIncidentRecordsDiagnosticsNotifiesOnceAndClearsOnRecovery(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.FeedSource{},
		&model.Subscription{},
		&model.FeedSourceDiagnostic{},
		&model.Notification{},
	)

	user := model.User{
		UUID:     uuid.New(),
		Username: "rss-health-user",
		Email:    "rss-health-user@example.test",
		Password: "hash",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	source := model.FeedSource{
		SourceType:               "external_rss",
		RssURL:                   "https://feeds.example.test/rss.xml",
		Hash:                     "rss-health-source",
		Title:                    "RSS Health Source",
		FetchConsecutiveFailures: 2,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: user.UUID, FeedSourceID: source.ID}).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	failedAt := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	group := rssSourceGroup{Sources: []model.FeedSource{source}}
	fetchErr := &rssFetchError{Code: "request_failed", Err: errors.New("upstream unavailable")}
	if err := markRSSFetchFailure(db, group, fetchErr, RSSFetchResult{}, failedAt); err != nil {
		t.Fatalf("record third fetch failure: %v", err)
	}

	var diagnostic model.FeedSourceDiagnostic
	if err := db.Where("feed_source_id = ? AND kind = ?", source.ID, "rss_fetch_failure").First(&diagnostic).Error; err != nil {
		t.Fatalf("load fetch failure diagnostic: %v", err)
	}
	if diagnostic.ErrorCode != "request_failed" || diagnostic.AttemptCount != 3 || !strings.Contains(diagnostic.Message, "upstream unavailable") {
		t.Fatalf("unexpected failure diagnostic: %#v", diagnostic)
	}

	var notificationCount int64
	if err := db.Model(&model.Notification{}).
		Where("recipient_id = ? AND type = ? AND source_id = ?", user.UUID, "feed_source_health", source.ID).
		Count(&notificationCount).Error; err != nil {
		t.Fatalf("count incident notifications: %v", err)
	}
	if notificationCount != 1 {
		t.Fatalf("incident notifications=%d, want 1", notificationCount)
	}

	if err := db.First(&source, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("reload source after failure: %v", err)
	}
	if err := markRSSFetchFailure(db, rssSourceGroup{Sources: []model.FeedSource{source}}, fetchErr, RSSFetchResult{}, failedAt.Add(time.Hour)); err != nil {
		t.Fatalf("record repeated fetch failure: %v", err)
	}
	if err := db.Model(&model.Notification{}).
		Where("recipient_id = ? AND type = ? AND source_id = ?", user.UUID, "feed_source_health", source.ID).
		Count(&notificationCount).Error; err != nil {
		t.Fatalf("count repeated incident notifications: %v", err)
	}
	if notificationCount != 1 {
		t.Fatalf("repeated incident notifications=%d, want 1", notificationCount)
	}

	if err := db.First(&source, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("reload source before recovery: %v", err)
	}
	recoveredAt := failedAt.Add(2 * time.Hour)
	if err := markRSSFetchSuccess(db, rssSourceGroup{Sources: []model.FeedSource{source}}, RSSFetchResult{Provider: rssFetchProviderDirect, HTTPStatus: 200}, recoveredAt, 1, false); err != nil {
		t.Fatalf("record fetch recovery: %v", err)
	}

	var recovery model.FeedSourceDiagnostic
	if err := db.Where("feed_source_id = ? AND kind = ?", source.ID, "rss_fetch_recovered").First(&recovery).Error; err != nil {
		t.Fatalf("load fetch recovery diagnostic: %v", err)
	}
	if recovery.AttemptCount != 4 || recovery.RecoveredAt == nil || !recovery.RecoveredAt.Equal(recoveredAt) {
		t.Fatalf("unexpected recovery diagnostic: %#v", recovery)
	}
	if err := db.Model(&model.Notification{}).
		Where("recipient_id = ? AND type = ? AND source_id = ?", user.UUID, "feed_source_health", source.ID).
		Count(&notificationCount).Error; err != nil {
		t.Fatalf("count cleared incident notifications: %v", err)
	}
	if notificationCount != 0 {
		t.Fatalf("incident notifications after recovery=%d, want 0", notificationCount)
	}
}
