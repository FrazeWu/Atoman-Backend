package feed

import (
	"encoding/json"
	"net/http"

	"atoman/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GetFeedPreferences godoc
// @Summary 获取订阅阅读偏好
// @Tags feed
// @Produce json
// @Success 200 "成功"
// @Router /api/v1/feed/preferences [get]
func GetFeedPreferences(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		pref := model.FeedPreference{UserID: userID, HiddenKeywords: json.RawMessage("[]")}
		db.FirstOrCreate(&pref, model.FeedPreference{UserID: userID})
		c.JSON(http.StatusOK, gin.H{"data": pref})
	}
}
// UpdateFeedPreferences godoc
// @Summary 更新订阅阅读偏好
// @Tags feed
// @Accept json
// @Produce json
// @Param input body object true "hidden_keywords 隐藏关键词列表"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/feed/preferences [put]
func UpdateFeedPreferences(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		var input struct {
			HiddenKeywords []string `json:"hidden_keywords"`
		}
		if c.ShouldBindJSON(&input) != nil {
			c.JSON(400, gin.H{"error": "invalid request"})
			return
		}
		raw, _ := json.Marshal(input.HiddenKeywords)
		pref := model.FeedPreference{UserID: userID, HiddenKeywords: raw}
		if err := db.Save(&pref).Error; err != nil {
			c.JSON(500, gin.H{"error": "save feed preferences failed"})
			return
		}
		c.JSON(200, gin.H{"data": pref})
	}
}
// BatchSubscribeFeedSources godoc
// @Summary 批量订阅来源
// @Tags feed
// @Accept json
// @Produce json
// @Param input body object true "批量订阅参数"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/feed/sources/batch-subscribe [post]
func BatchSubscribeFeedSources(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		var input struct {
			SourceIDs []uuid.UUID `json:"source_ids"`
		}
		if c.ShouldBindJSON(&input) != nil || len(input.SourceIDs) == 0 {
			c.JSON(400, gin.H{"error": "invalid request"})
			return
		}
		group, err := getOrCreateDefaultSubscriptionGroup(db, userID)
		if err != nil {
			c.JSON(500, gin.H{"error": "prepare group failed"})
			return
		}
		created := 0
		createdSubscriptions := []model.Subscription{}
		createdIDs := []uuid.UUID{}
		reusedIDs := []uuid.UUID{}
		missingIDs := []uuid.UUID{}
		for _, sourceID := range input.SourceIDs {
			var source model.FeedSource
			if db.First(&source, "id = ? AND hidden = ?", sourceID, false).Error != nil {
				missingIDs = append(missingIDs, sourceID)
				continue
			}
			sub := model.Subscription{
				UserID:              userID,
				FeedSourceID:        source.ID,
				Title:               source.Title,
				SubscriptionGroupID: &group.ID,
				Position:            nextSubscriptionPosition(db, userID, &group.ID),
			}
			createdSubscription, wasCreated, createErr := ensureSubscription(db, sub)
			if createErr != nil {
				c.JSON(500, gin.H{"error": "create subscription failed"})
				return
			}
			if wasCreated {
				created++
				createdSubscriptions = append(createdSubscriptions, createdSubscription)
				createdIDs = append(createdIDs, sourceID)
			} else {
				reusedIDs = append(reusedIDs, sourceID)
			}
		}
		for _, subscription := range createdSubscriptions {
			applySubscriptionRulesForSubscription(db, subscription)
		}
		c.JSON(200, gin.H{"data": gin.H{"created": created, "created_ids": createdIDs, "reused_ids": reusedIDs, "missing_ids": missingIDs}})
	}
}

var _ *gorm.DB
