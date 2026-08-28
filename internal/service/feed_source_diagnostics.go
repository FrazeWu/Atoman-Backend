package service

import (
	"fmt"
	"log"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	feedSourceDiagnosticRetention                = 90 * 24 * time.Hour
	RSSFetchFailureDiagnosticKind                = "rss_fetch_failure"
	RSSFetchRecoveredDiagnosticKind              = "rss_fetch_recovered"
	feedSourceHealthNotificationType             = "feed_source_health"
	feedSourceHealthNotificationSourceType       = "feed_source_health"
	feedSourceHealthNotificationFailureThreshold = 3
)

func recordRSSFetchFailureOperations(db *gorm.DB, group rssSourceGroup, code, message string, attempt int) {
	diagnostics := make([]model.FeedSourceDiagnostic, 0, len(group.Sources))
	for _, source := range group.Sources {
		diagnostics = append(diagnostics, model.FeedSourceDiagnostic{
			FeedSourceID: source.ID,
			Kind:         RSSFetchFailureDiagnosticKind,
			ErrorCode:    code,
			Message:      message,
			AttemptCount: attempt,
		})
	}
	if err := recordRSSFetchDiagnostics(db, diagnostics); err != nil {
		log.Printf("failed to record RSS fetch diagnostics: %v", err)
	}
	if attempt != feedSourceHealthNotificationFailureThreshold {
		return
	}
	if err := createRSSFetchIncidentNotifications(db, group.Sources, code, message, attempt); err != nil {
		log.Printf("failed to create RSS fetch incident notifications: %v", err)
	}
}

func recordRSSFetchDiagnostics(db *gorm.DB, diagnostics []model.FeedSourceDiagnostic) error {
	if len(diagnostics) == 0 {
		return nil
	}
	if err := db.CreateInBatches(&diagnostics, 100).Error; err != nil {
		return err
	}
	return db.Unscoped().Where("created_at < ?", time.Now().UTC().Add(-feedSourceDiagnosticRetention)).Delete(&model.FeedSourceDiagnostic{}).Error
}

func recordRSSFetchRecoveryOperations(db *gorm.DB, group rssSourceGroup, recoveredAt time.Time) {
	diagnostics := make([]model.FeedSourceDiagnostic, 0, len(group.Sources))
	sourceIDs := make([]uuid.UUID, 0, len(group.Sources))
	for _, source := range group.Sources {
		if source.FetchConsecutiveFailures == 0 {
			continue
		}
		diagnostics = append(diagnostics, model.FeedSourceDiagnostic{
			FeedSourceID: source.ID,
			Kind:         RSSFetchRecoveredDiagnosticKind,
			ErrorCode:    source.FetchLastErrorCode,
			Message:      "RSS fetch recovered",
			AttemptCount: source.FetchConsecutiveFailures,
			RecoveredAt:  &recoveredAt,
		})
		sourceIDs = append(sourceIDs, source.ID)
	}
	if err := recordRSSFetchDiagnostics(db, diagnostics); err != nil {
		log.Printf("failed to record RSS fetch recoveries: %v", err)
	}
	if err := clearRSSFetchIncidentNotifications(db, sourceIDs); err != nil {
		log.Printf("failed to clear RSS fetch incident notifications: %v", err)
	}
}

func createRSSFetchIncidentNotifications(db *gorm.DB, sources []model.FeedSource, code, message string, attempt int) error {
	sourceLabels := make(map[uuid.UUID]string, len(sources))
	sourceIDs := make([]uuid.UUID, 0, len(sources))
	for _, source := range sources {
		if source.ID == uuid.Nil {
			continue
		}
		sourceTitle := source.Title
		if sourceTitle == "" {
			sourceTitle = "订阅源"
		}
		sourceLabels[source.ID] = sourceTitle
		sourceIDs = append(sourceIDs, source.ID)
	}
	if len(sourceIDs) == 0 {
		return nil
	}

	type subscriptionRecipient struct {
		UserID       uuid.UUID
		FeedSourceID uuid.UUID
	}
	var recipients []subscriptionRecipient
	if err := db.Model(&model.Subscription{}).
		Select("user_id", "feed_source_id").
		Where("feed_source_id IN ? AND is_paused = ?", sourceIDs, false).
		Find(&recipients).Error; err != nil {
		return err
	}

	var existing []model.Notification
	if err := db.Select("recipient_id", "source_id").
		Where("type = ? AND source_type = ? AND source_id IN ?", feedSourceHealthNotificationType, feedSourceHealthNotificationSourceType, sourceIDs).
		Find(&existing).Error; err != nil {
		return err
	}
	existingByRecipientAndSource := make(map[string]struct{}, len(existing))
	for _, notification := range existing {
		existingByRecipientAndSource[notification.RecipientID.String()+":"+notification.SourceID.String()] = struct{}{}
	}

	notifications := make([]model.Notification, 0, len(recipients))
	for _, recipient := range recipients {
		key := recipient.UserID.String() + ":" + recipient.FeedSourceID.String()
		if _, found := existingByRecipientAndSource[key]; found {
			continue
		}
		sourceTitle := sourceLabels[recipient.FeedSourceID]
		notifications = append(notifications, model.Notification{
			RecipientID: recipient.UserID,
			Type:        feedSourceHealthNotificationType,
			SourceType:  feedSourceHealthNotificationSourceType,
			SourceID:    recipient.FeedSourceID,
			Meta: model.NotificationMeta{
				"title":        "订阅源需要处理",
				"body":         fmt.Sprintf("%s 已连续抓取失败 %d 次（%s），系统会自动重试。", sourceTitle, attempt, message),
				"source_label": sourceTitle,
				"path":         "/feed?manage_subscriptions=1&manage_tab=sources",
			},
		})
	}
	if len(notifications) == 0 {
		return nil
	}
	return db.CreateInBatches(&notifications, 100).Error
}

func clearRSSFetchIncidentNotifications(db *gorm.DB, sourceIDs []uuid.UUID) error {
	if len(sourceIDs) == 0 {
		return nil
	}
	return db.Where("type = ? AND source_type = ? AND source_id IN ?", feedSourceHealthNotificationType, feedSourceHealthNotificationSourceType, sourceIDs).
		Delete(&model.Notification{}).Error
}

func recordFeedSourceDiagnostic(db *gorm.DB, sourceID uuid.UUID, itemID *uuid.UUID, kind, errorCode, message string, attemptCount int, recoveredAt *time.Time) error {
	now := time.Now().UTC()
	entry := model.FeedSourceDiagnostic{
		FeedSourceID: sourceID,
		FeedItemID:   itemID,
		Kind:         kind,
		ErrorCode:    errorCode,
		Message:      message,
		AttemptCount: attemptCount,
		RecoveredAt:  recoveredAt,
	}
	if err := db.Create(&entry).Error; err != nil {
		return err
	}
	return db.Unscoped().Where("created_at < ?", now.Add(-feedSourceDiagnosticRetention)).Delete(&model.FeedSourceDiagnostic{}).Error
}
