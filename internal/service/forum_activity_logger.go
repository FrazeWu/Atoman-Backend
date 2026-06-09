package service

import (
	"log"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
)

// LogActivity asynchronously writes a record to activity_logs.
// Failures are logged but never propagate to the caller.
func LogActivity(db *gorm.DB, userID uuid.UUID, action, targetType string, targetID uuid.UUID) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("WARN: LogActivity panic recovered: %v", r)
			}
		}()
		entry := model.ActivityLog{
			UserID:     userID,
			Action:     action,
			TargetType: targetType,
			TargetID:   targetID,
		}
		if err := db.Create(&entry).Error; err != nil {
			log.Printf("WARN: failed to log activity (%s): %v", action, err)
		}
	}()
}

// HasRecentActivity checks whether a user has logged a given action against a
// target within the past `windowSeconds` seconds. Used to deduplicate view counts.
func HasRecentActivity(db *gorm.DB, userID uuid.UUID, action string, targetID uuid.UUID, windowSeconds int) bool {
	var count int64
	db.Model(&model.ActivityLog{}).
		Where(
			"user_id = ? AND action = ? AND target_id = ? AND created_at > NOW() - INTERVAL '? seconds'",
			userID, action, targetID, windowSeconds,
		).
		Count(&count)
	return count > 0
}
