package service

import (
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const feedSourceDiagnosticRetention = 90 * 24 * time.Hour

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
