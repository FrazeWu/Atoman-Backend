package feed

import (
	"net/http"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func filterRecommendationItemsForUser(db *gorm.DB, userID *uuid.UUID, items []RecommendationItemDTO, targetType string) ([]RecommendationItemDTO, int64) {
	if userID == nil || len(items) == 0 {
		return items, int64(len(items))
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		if id, err := uuid.Parse(item.ID); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return items, int64(len(items))
	}
	var blocked []uuid.UUID
	if err := db.Model(&model.RecommendationFeedback{}).
		Where("user_id = ? AND target_type = ? AND target_id IN ? AND action IN ?", *userID, targetType, ids, []string{"hide", "less_source"}).
		Pluck("target_id", &blocked).Error; err != nil {
		return items, int64(len(items))
	}
	blockedSet := make(map[uuid.UUID]struct{}, len(blocked))
	for _, id := range blocked {
		blockedSet[id] = struct{}{}
	}
	filtered := make([]RecommendationItemDTO, 0, len(items))
	for _, item := range items {
		id, err := uuid.Parse(item.ID)
		if err != nil {
			continue
		}
		if _, ok := blockedSet[id]; !ok {
			filtered = append(filtered, item)
		}
	}
	return filtered, int64(len(filtered))
}

type recommendationFeedbackInput struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Action     string `json:"action"`
}

func (h *Handler) recordRecommendationFeedback(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var input recommendationFeedbackInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "invalid recommendation feedback"))
		return
	}
	targetType := strings.ToLower(strings.TrimSpace(input.TargetType))
	action := strings.ToLower(strings.TrimSpace(input.Action))
	targetID, err := uuid.Parse(strings.TrimSpace(input.TargetID))
	if !validRecommendationFeedback(targetType, action) || err != nil || targetID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "target_type, target_id or action is invalid"))
		return
	}
	feedback := model.RecommendationFeedback{UserID: user.ID, TargetType: targetType, TargetID: targetID, Action: action}
	if err := h.service.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ? AND target_type = ? AND target_id = ?", user.ID, targetType, targetID).Delete(&model.RecommendationFeedback{}).Error; err != nil {
			return err
		}
		return tx.Create(&feedback).Error
	}); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) deleteRecommendationFeedback(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	targetType := strings.ToLower(strings.TrimSpace(c.Param("target_type")))
	targetID, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil || targetID == uuid.Nil || !validRecommendationFeedbackTarget(targetType) {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "target_type or target_id is invalid"))
		return
	}
	if err := h.service.db.Where("user_id = ? AND target_type = ? AND target_id = ?", user.ID, targetType, targetID).Delete(&model.RecommendationFeedback{}).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func validRecommendationFeedbackTarget(targetType string) bool {
	switch targetType {
	case "article", "channel":
		return true
	default:
		return false
	}
}

func validRecommendationFeedback(targetType, action string) bool {
	if !validRecommendationFeedbackTarget(targetType) {
		return false
	}
	switch action {
	case "hide", "less_source", "block_tag":
		return true
	default:
		return false
	}
}
