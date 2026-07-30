package feed

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"time"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CheckSubscriptionHealth checks the health of a specific subscription by attempting to fetch the feed
// CheckSubscriptionHealth godoc
// @Summary 检查单个订阅健康状态
// @Description 检测订阅源可访问性并更新健康状态。
// @Tags feed
// @Produce json
// @Param id path string true "订阅 UUID"
// @Success 200 {object} FeedHealthCheckResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/{id}/health [post]
func CheckSubscriptionHealth(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		subscriptionIDStr := c.Param("id")
		subscriptionID, err := uuid.Parse(subscriptionIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid subscription ID"})
			return
		}

		// Get subscription
		var subscription model.Subscription
		if err := db.Preload("FeedSource").First(&subscription, "id = ?", subscriptionID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Subscription not found"})
			return
		}

		// Verify ownership
		if subscription.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
			return
		}

		// Internal subscriptions (user/channel/collection) read directly from DB — no network check needed
		if subscription.FeedSource != nil && subscription.FeedSource.SourceType != "external_rss" {
			now := time.Now()
			subscription.HealthStatus = "healthy"
			subscription.ErrorMessage = ""
			subscription.LastChecked = &now
			db.Save(&subscription)
			c.JSON(http.StatusOK, gin.H{
				"subscription_id": subscriptionID.String(),
				"health_status":   "healthy",
				"error_message":   "",
				"last_checked":    subscription.LastChecked,
				"skipped":         true,
				"reason":          "internal subscription — no external URL to check",
			})
			return
		}

		// Attempt to fetch the external RSS feed
		healthStatus := "healthy"
		errorMessage := ""

		resp, err := http.Get(subscription.FeedSource.RssURL)
		if err != nil {
			healthStatus = "error"
			errorMessage = fmt.Sprintf("Failed to fetch feed: %v", err)
		} else if resp.StatusCode >= 400 {
			healthStatus = "warning"
			errorMessage = fmt.Sprintf("HTTP status: %d", resp.StatusCode)
		} else {
			defer resp.Body.Close()
			// Try to parse XML to verify it's valid
			decoder := xml.NewDecoder(resp.Body)
			_, err := decoder.Token()
			if err != nil {
				healthStatus = "error"
				errorMessage = fmt.Sprintf("Invalid XML: %v", err)
			}
		}

		// Update subscription health status
		subscription.HealthStatus = healthStatus
		subscription.ErrorMessage = errorMessage
		now := time.Now()
		subscription.LastChecked = &now
		db.Save(&subscription)

		c.JSON(http.StatusOK, gin.H{
			"subscription_id": subscriptionID.String(),
			"health_status":   healthStatus,
			"error_message":   errorMessage,
			"last_checked":    subscription.LastChecked,
		})
	}
}

// CheckAllSubscriptionsHealth checks health of all user's subscriptions
// CheckAllSubscriptionsHealth godoc
// @Summary 检查全部订阅健康状态
// @Description 批量检测当前用户所有订阅的健康状态。
// @Tags feed
// @Produce json
// @Success 200 {object} FeedHealthCheckListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/health/check-all [post]
func CheckAllSubscriptionsHealth(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var subscriptions []model.Subscription
		if err := db.Where("user_id = ?", userID).Find(&subscriptions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch subscriptions"})
			return
		}

		results := make([]gin.H, 0)
		for _, sub := range subscriptions {
			// Fetch feed source
			var source model.FeedSource
			if err := db.First(&source, sub.FeedSourceID).Error; err != nil {
				continue
			}

			// Internal subscriptions read directly from DB — skip network check, always healthy
			if source.SourceType != "external_rss" {
				now := time.Now()
				sub.HealthStatus = "healthy"
				sub.ErrorMessage = ""
				sub.LastChecked = &now
				db.Save(&sub)
				results = append(results, gin.H{
					"subscription_id": sub.ID,
					"health_status":   "healthy",
					"error_message":   "",
					"skipped":         true,
				})
				continue
			}

			healthStatus := "healthy"
			errorMessage := ""

			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Get(source.RssURL)
			if err != nil {
				healthStatus = "error"
				errorMessage = fmt.Sprintf("Failed to fetch: %v", err)
			} else if resp.StatusCode >= 400 {
				healthStatus = "warning"
				errorMessage = fmt.Sprintf("HTTP %d", resp.StatusCode)
			} else {
				defer resp.Body.Close()
			}

			// Update subscription
			sub.HealthStatus = healthStatus
			sub.ErrorMessage = errorMessage
			now := time.Now()
			sub.LastChecked = &now
			db.Save(&sub)

			results = append(results, gin.H{
				"subscription_id": sub.ID,
				"health_status":   healthStatus,
				"error_message":   errorMessage,
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"checked_count": len(results),
			"results":       results,
		})
	}
}
