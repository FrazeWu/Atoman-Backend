package handlers

import (
	"net/http"
	"strings"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type videoRecommendationFeedbackInput struct {
	Scope    string    `json:"scope" binding:"required"`
	TargetID uuid.UUID `json:"target_id" binding:"required"`
}

func CreateVideoRecommendationFeedback(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input videoRecommendationFeedbackInput
		if err := c.ShouldBindJSON(&input); err != nil || !validVideoRecommendationScope(input.Scope) || input.TargetID == uuid.Nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scope and target_id are invalid"})
			return
		}
		userID := currentBlogViewerID(c)
		if userID == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		feedback := model.VideoRecommendationFeedback{UserID: *userID, Scope: strings.ToLower(strings.TrimSpace(input.Scope)), TargetID: input.TargetID}
		if err := db.Where(feedback).FirstOrCreate(&feedback).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save recommendation feedback"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func DeleteVideoRecommendationFeedback(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		targetID, err := uuid.Parse(c.Param("id"))
		scope := strings.ToLower(c.Param("scope"))
		if err != nil || !validVideoRecommendationScope(scope) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scope and id are invalid"})
			return
		}
		userID := currentBlogViewerID(c)
		if userID == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		if err := db.Where("user_id = ? AND scope = ? AND target_id = ?", *userID, scope, targetID).Delete(&model.VideoRecommendationFeedback{}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to restore recommendation"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func validVideoRecommendationScope(scope string) bool {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "video", "channel", "tag":
		return true
	default:
		return false
	}
}

type videoRecommendationFeedbackSet struct {
	hiddenVideos    map[uuid.UUID]bool
	reducedChannels map[uuid.UUID]bool
	blockedTags     map[uuid.UUID]bool
}

func videoRecommendationFeedbackFor(db *gorm.DB, userID *uuid.UUID) videoRecommendationFeedbackSet {
	set := videoRecommendationFeedbackSet{hiddenVideos: map[uuid.UUID]bool{}, reducedChannels: map[uuid.UUID]bool{}, blockedTags: map[uuid.UUID]bool{}}
	if userID == nil || !db.Migrator().HasTable(&model.VideoRecommendationFeedback{}) {
		return set
	}
	var rows []model.VideoRecommendationFeedback
	if db.Where("user_id = ?", *userID).Find(&rows).Error != nil {
		return set
	}
	for _, row := range rows {
		switch row.Scope {
		case "video":
			set.hiddenVideos[row.TargetID] = true
		case "channel":
			set.reducedChannels[row.TargetID] = true
		case "tag":
			set.blockedTags[row.TargetID] = true
		}
	}
	return set
}

func (set videoRecommendationFeedbackSet) excludes(video model.Video) bool {
	if set.hiddenVideos[video.ID] {
		return true
	}
	for _, tag := range video.Tags {
		if set.blockedTags[tag.ID] {
			return true
		}
	}
	return false
}
