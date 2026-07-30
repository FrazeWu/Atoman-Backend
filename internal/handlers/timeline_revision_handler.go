package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"atoman/internal/model"
)

func saveEventRevision(db *gorm.DB, event model.TimelineEvent, editorID uuid.UUID) {
	endDate := ""
	if event.EndDate != nil {
		endDate = event.EndDate.Format("2006-01-02")
	}
	rev := model.TimelineRevision{
		EventID:     event.ID,
		EditorID:    editorID,
		Title:       event.Title,
		Description: event.Description,
		Content:     event.Content,
		EventDate:   event.EventDate.Format("2006-01-02"),
		EndDate:     endDate,
		Location:    event.Location,
		Latitude:    event.Latitude,
		Longitude:   event.Longitude,
		Source:      event.Source,
		Category:    event.Category,
		Tags:        append(pq.StringArray(nil), event.Tags...),
		IsPublic:    event.IsPublic,
	}
	db.Create(&rev)
}

// GetTimelineEventHistory returns revision history for an event.
// Route: GET /api/timeline/events/:id/history
// GetTimelineEventHistory godoc
// @Summary 获取事件修订历史
// @Description 返回时间线事件的修订快照列表。
// @Tags timeline
// @Produce json
// @Param id path string true "事件 UUID"
// @Success 200 {object} TimelineRevisionListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/timeline/events/{id}/history [get]
func GetTimelineEventHistory(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var revisions []model.TimelineRevision
		if err := db.Preload("Editor").Where("event_id = ?", id).Order("created_at DESC").Find(&revisions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch history"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": revisions})
	}
}

// RevertTimelineEvent reverts an event to a specific revision (admin only).
// Route: POST /api/timeline/events/:id/revert/:revision_id
// RevertTimelineEvent godoc
// @Summary 回滚时间线事件
// @Description 管理员将时间线事件回滚到指定修订版本。
// @Tags timeline
// @Produce json
// @Param id path string true "事件 UUID"
// @Param revision_id path string true "修订 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/timeline/events/{id}/revert/{revision_id} [post]
func RevertTimelineEvent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		if role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin only"})
			return
		}

		id := c.Param("id")
		revID := c.Param("revision_id")

		var rev model.TimelineRevision
		if err := db.First(&rev, "id = ? AND event_id = ?", revID, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Revision not found"})
			return
		}

		eventDate, _ := parseDateTime(rev.EventDate)
		updates := map[string]interface{}{
			"title":       rev.Title,
			"description": rev.Description,
			"content":     rev.Content,
			"event_date":  eventDate,
			"location":    rev.Location,
			"latitude":    rev.Latitude,
			"longitude":   rev.Longitude,
			"source":      rev.Source,
			"category":    rev.Category,
			"tags":        rev.Tags,
			"is_public":   rev.IsPublic,
		}
		if rev.EndDate != "" {
			if endDate, err := parseDateTime(rev.EndDate); err == nil {
				updates["end_date"] = endDate
			}
		} else {
			updates["end_date"] = nil
		}

		if err := db.Model(&model.TimelineEvent{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to revert event"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "reverted"})
	}
}
