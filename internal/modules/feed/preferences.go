package feed

import (
	"encoding/json"
	"net/http"

	"atoman/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func GetFeedPreferences(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		pref := model.FeedPreference{UserID: userID, HiddenKeywords: json.RawMessage("[]")}
		db.FirstOrCreate(&pref, model.FeedPreference{UserID: userID})
		c.JSON(http.StatusOK, gin.H{"data": pref})
	}
}
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
		for _, sourceID := range input.SourceIDs {
			var source model.FeedSource
			if db.First(&source, "id = ? AND hidden = ?", sourceID, false).Error != nil {
				continue
			}
			sub := model.Subscription{UserID: userID, FeedSourceID: source.ID, Title: source.Title, SubscriptionGroupID: &group.ID}
			result := db.Where("user_id = ? AND feed_source_id = ?", userID, source.ID).FirstOrCreate(&sub)
			if result.Error == nil && result.RowsAffected > 0 {
				created++
			}
		}
		applySubscriptionRulesForUser(db, userID)
		c.JSON(200, gin.H{"data": gin.H{"created": created}})
	}
}

var _ *gorm.DB
