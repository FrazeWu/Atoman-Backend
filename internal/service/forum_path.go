package service

import (
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
)

// BuildReplyPath assigns a flat, chronological floor number for a new reply.
// parentReplyID is intentionally ignored so quote-style replies do not nest.
func BuildReplyPath(db *gorm.DB, topicID uuid.UUID, _ *uuid.UUID) (path string, floor int, err error) {
	err = db.Transaction(func(tx *gorm.DB) error {
		// Lock the topic row to serialize concurrent reply creation
		var topic model.ForumTopic
		if err := tx.Set("gorm:query_option", "FOR UPDATE").
			First(&topic, "id = ?", topicID).Error; err != nil {
			return fmt.Errorf("topic not found: %w", err)
		}

		// Compute next floor number (1-based, global per topic)
		var maxFloor int
		tx.Model(&model.ForumReply{}).
			Where("topic_id = ? AND deleted_at IS NULL", topicID).
			Select("COALESCE(MAX(floor_number), 0)").
			Scan(&maxFloor)
		floor = maxFloor + 1

		// Keep path flat so every reply renders as a top-level item.
		paddedFloor := fmt.Sprintf("%06d", floor)
		path = paddedFloor
		return nil
	})
	return path, floor, err
}

// FormatFloor returns a display-friendly floor label like "#42"
func FormatFloor(n int) string {
	return fmt.Sprintf("#%d", n)
}

// PathPrefix returns a LIKE pattern to match all descendants of a path.
func PathPrefix(path string) string {
	return path + ".%"
}
