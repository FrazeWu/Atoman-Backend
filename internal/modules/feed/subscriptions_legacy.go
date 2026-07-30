package feed

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"atoman/internal/model"
	"atoman/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateSubscription godoc
// @Summary 创建订阅
// @Description 创建站内或外部 RSS 订阅。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body SubscriptionInput true "订阅输入"
// @Success 201 {object} SubscriptionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions [post]
func CreateSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input SubscriptionInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// 检测内部 RSS URL（/api/feed/rss/:username），自动转换为 internal_user 类型
		if input.TargetType == "external_rss" && input.RssURL != "" {
			if userID, err := resolveInternalRSSURL(db, input.RssURL); err == nil {
				input.TargetType = "internal_user"
				input.TargetID = &userID
				input.RssURL = ""
			}
		}

		if input.TargetType != "external_rss" && input.TargetID == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "target_id is required for internal subscriptions"})
			return
		}
		if input.TargetType == "external_rss" && input.RssURL == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "rss_url is required for external subscriptions"})
			return
		}
		if input.TargetType == "external_rss" {
			if u, err := url.ParseRequestURI(input.RssURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "rss_url must be an absolute http/https URL"})
				return
			}
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var subscriptionGroupID *uuid.UUID
		if input.TargetType == "external_rss" || input.TargetType == "internal_user" {
			defaultGroup, err := getOrCreateDefaultSubscriptionGroup(db, userID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare default group"})
				return
			}
			subscriptionGroupID = &defaultGroup.ID
		}

		provider := ""
		if input.TargetType == "external_rss" {
			if override, ok := c.Get("provider_override"); ok {
				if value, ok := override.(string); ok {
					provider = strings.TrimSpace(value)
				}
			}
		}

		source, err := findOrCreateFeedSource(db, input.TargetType, input.TargetID, input.RssURL, input.Title, provider)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create feed source"})
			return
		}

		var existingSub model.Subscription
		if err := db.Where("user_id = ? AND feed_source_id = ?", userID, source.ID).First(&existingSub).Error; err == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Already subscribed to this source"})
			return
		}

		subscription := model.Subscription{
			UserID:              userID,
			FeedSourceID:        source.ID,
			Title:               input.Title,
			SubscriptionGroupID: subscriptionGroupID,
		}

		result := db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}, {Name: "feed_source_id"}},
			TargetWhere: clause.Where{Exprs: []clause.Expression{
				clause.Eq{Column: clause.Column{Name: "deleted_at"}, Value: nil},
			}},
			DoNothing: true,
		}).Create(&subscription)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create subscription"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Already subscribed to this source"})
			return
		}
		applySubscriptionRulesForSubscription(db, subscription)

		if input.TargetType == "external_rss" {
			syncFeedSource(db, *source)
		}

		c.JSON(http.StatusCreated, gin.H{"data": subscription, "message": "ok"})
	}
}

func CreateSubscriptionFromProvider(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input struct {
			Provider    string            `json:"provider" binding:"required"`
			TemplateKey string            `json:"template_key" binding:"required"`
			Params      map[string]string `json:"params"`
			Title       string            `json:"title"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}
		if strings.TrimSpace(input.Provider) != "rsshub" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported provider"})
			return
		}

		feedURL, err := service.BuildRSSHubFeedURL(input.TemplateKey, input.Params)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.Set("provider_override", "rsshub")
		c.Request.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(`{"target_type":"external_rss","rss_url":%q,"title":%q}`, feedURL, input.Title)))
		CreateSubscription(db)(c)
	}
}

// DeleteSubscription godoc
// @Summary 删除订阅
// @Description 删除当前用户的一个订阅。
// @Tags feed
// @Produce json
// @Param id path string true "订阅 UUID"
// @Success 200 {object} MessageResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/{id} [delete]
func DeleteSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if err := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.Subscription{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete subscription"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// UpdateSubscription godoc
// @Summary 更新订阅
// @Description 更新当前用户订阅的显示标题或分组。
// @Tags feed
// @Accept json
// @Produce json
// @Param id path string true "订阅 UUID"
// @Param input body object true "订阅更新输入"
// @Success 200 {object} object
// @Failure 400 {object} object
// @Failure 404 {object} object
// @Failure 500 {object} object
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/{id} [put]
func UpdateSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid subscription id"})
			return
		}

		var input struct {
			Title              *string    `json:"title"`
			GroupID            *uuid.UUID `json:"group_id"`
			IsMuted            *bool      `json:"is_muted"`
			AutoMarkRead       *bool      `json:"auto_mark_read"`
			AutoAddReadingList *bool      `json:"auto_add_reading_list"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}

		var sub model.Subscription
		if err := db.Where("id = ? AND user_id = ?", id, userID).First(&sub).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Subscription not found"})
			return
		}

		updates := map[string]interface{}{}
		if input.Title != nil {
			updates["title"] = strings.TrimSpace(*input.Title)
		}
		if input.GroupID != nil {
			var group model.SubscriptionGroup
			if err := db.Where("id = ? AND user_id = ?", *input.GroupID, userID).First(&group).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Subscription group not found"})
				return
			}
			updates["subscription_group_id"] = *input.GroupID
		}
		if input.IsMuted != nil {
			updates["is_muted"] = *input.IsMuted
		}
		if input.AutoMarkRead != nil {
			updates["auto_mark_read"] = *input.AutoMarkRead
		}
		if input.AutoAddReadingList != nil {
			updates["auto_add_reading_list"] = *input.AutoAddReadingList
		}
		if len(updates) > 0 {
			if err := db.Model(&sub).Updates(updates).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update subscription"})
				return
			}
		}
		if err := db.Preload("FeedSource").Preload("SubscriptionGroup").First(&sub, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload subscription"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": sub, "message": "ok"})
	}
}

// GetSubscriptions godoc
// @Summary 获取订阅列表
// @Description 返回当前用户的订阅列表。
// @Tags feed
// @Produce json
// @Success 200 {object} SubscriptionListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions [get]
func GetSubscriptions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		defaultGroup, err := getOrCreateDefaultSubscriptionGroup(db, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare default group"})
			return
		}

		// Keep old data compatible: migrate NULL group subscriptions to default group.
		if err := db.Model(&model.Subscription{}).
			Where("user_id = ? AND subscription_group_id IS NULL", userID).
			Update("subscription_group_id", defaultGroup.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to normalize subscriptions"})
			return
		}

		var subscriptions []model.Subscription
		if err := db.Preload("FeedSource").Preload("SubscriptionGroup").Where("user_id = ?", userID).Find(&subscriptions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch subscriptions"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": subscriptions, "message": "ok"})
	}
}
