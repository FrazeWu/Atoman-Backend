package feed

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SubscribeChannel subscribes the current user to a channel
// SubscribeChannel godoc
// @Summary 订阅频道
// @Description 将当前用户订阅到指定频道。
// @Tags feed
// @Produce json
// @Param channel_id path string true "频道 UUID"
// @Success 200 {object} SubscriptionActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscribe/channel/{channel_id} [post]
func SubscribeChannel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		channelIDStr := c.Param("channel_id")
		channelID, err := uuid.Parse(channelIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel ID"})
			return
		}

		feedSource, title, found, err := resolveChannelSubscriptionFeedSource(db, channelID, true)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve subscription source"})
			return
		}
		if !found {
			c.JSON(http.StatusNotFound, gin.H{"error": "Channel not found"})
			return
		}

		// Check if already subscribed
		var existingSub model.Subscription
		if err := db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).First(&existingSub).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "Already subscribed"})
			return
		}

		// Create subscription
		subscription := model.Subscription{
			UserID:       userID,
			FeedSourceID: feedSource.ID,
			Title:        title,
		}
		if err := db.Create(&subscription).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create subscription"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Subscribed successfully", "subscription": subscription})
	}
}

func resolveChannelSubscriptionFeedSource(db *gorm.DB, id uuid.UUID, createForChannel bool) (model.FeedSource, string, bool, error) {
	var channel model.Channel
	channelErr := db.First(&channel, "id = ?", id).Error
	if channelErr == nil {
		hashStr := fmt.Sprintf("internal_channel:%s", id.String())
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashStr)))
		var feedSource model.FeedSource
		if err := db.Where("hash = ?", hash).First(&feedSource).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return model.FeedSource{}, "", false, err
			}
			if !createForChannel {
				return model.FeedSource{}, "", false, nil
			}
			feedSource = model.FeedSource{
				SourceType: "internal_channel",
				SourceID:   &id,
				Title:      channel.Name,
				Hash:       hash,
			}
			if err := db.Create(&feedSource).Error; err != nil {
				return model.FeedSource{}, "", false, err
			}
		}
		return feedSource, channel.Name, true, nil
	}
	if !errors.Is(channelErr, gorm.ErrRecordNotFound) {
		return model.FeedSource{}, "", false, channelErr
	}

	var feedSource model.FeedSource
	if err := db.First(&feedSource, "id = ?", id).Error; err == nil {
		return feedSource, feedSource.Title, true, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.FeedSource{}, "", false, err
	}

	return model.FeedSource{}, "", false, nil
}

// UnsubscribeChannel unsubscribes the current user from a channel
// UnsubscribeChannel godoc
// @Summary 取消订阅频道
// @Description 将当前用户从指定频道取消订阅。
// @Tags feed
// @Produce json
// @Param channel_id path string true "频道 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscribe/channel/{channel_id} [delete]
func UnsubscribeChannel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		channelIDStr := c.Param("channel_id")
		channelID, err := uuid.Parse(channelIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel ID"})
			return
		}

		feedSource, _, found, err := resolveChannelSubscriptionFeedSource(db, channelID, false)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve subscription source"})
			return
		}
		if !found {
			c.JSON(http.StatusNotFound, gin.H{"error": "Subscription not found"})
			return
		}

		// Delete subscription
		if err := db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).Delete(&model.Subscription{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unsubscribe"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Unsubscribed successfully"})
	}
}

// SubscribeCollection subscribes the current user to a collection
// SubscribeCollection godoc
// @Summary 订阅合集
// @Description 将当前用户订阅到指定合集。
// @Tags feed
// @Produce json
// @Param collection_id path string true "合集 UUID"
// @Success 200 {object} SubscriptionActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscribe/collection/{collection_id} [post]
func SubscribeCollection(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		collectionIDStr := c.Param("collection_id")
		collectionID, err := uuid.Parse(collectionIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid collection ID"})
			return
		}

		// Verify collection exists
		var collection model.Collection
		if err := db.First(&collection, "id = ?", collectionID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Collection not found"})
			return
		}

		// Find or create FeedSource for this collection
		var feedSource model.FeedSource
		hashStr := fmt.Sprintf("internal_collection:%s", collectionIDStr)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashStr)))

		if err := db.Where("hash = ?", hash).First(&feedSource).Error; err != nil {
			// Create new FeedSource
			feedSource = model.FeedSource{
				SourceType: "internal_collection",
				SourceID:   &collectionID,
				Title:      collection.Name,
				Hash:       hash,
			}
			if err := db.Create(&feedSource).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create feed source"})
				return
			}
		}

		// Check if already subscribed
		var existingSub model.Subscription
		if err := db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).First(&existingSub).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "Already subscribed"})
			return
		}

		// Create subscription
		subscription := model.Subscription{
			UserID:       userID,
			FeedSourceID: feedSource.ID,
			Title:        collection.Name,
		}
		if err := db.Create(&subscription).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create subscription"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Subscribed successfully", "subscription": subscription})
	}
}

// UnsubscribeCollection unsubscribes the current user from a collection
// UnsubscribeCollection godoc
// @Summary 取消订阅合集
// @Description 将当前用户从指定合集取消订阅。
// @Tags feed
// @Produce json
// @Param collection_id path string true "合集 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscribe/collection/{collection_id} [delete]
func UnsubscribeCollection(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		collectionIDStr := c.Param("collection_id")
		_, err := uuid.Parse(collectionIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid collection ID"})
			return
		}

		// Find FeedSource for this collection
		var feedSource model.FeedSource
		hashStr := fmt.Sprintf("internal_collection:%s", collectionIDStr)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashStr)))

		if err := db.Where("hash = ?", hash).First(&feedSource).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Subscription not found"})
			return
		}

		// Delete subscription
		if err := db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).Delete(&model.Subscription{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unsubscribe"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Unsubscribed successfully"})
	}
}

// CheckChannelSubscription checks if the current user is subscribed to a channel
// CheckChannelSubscription godoc
// @Summary 查询频道订阅状态
// @Description 返回当前用户是否已订阅指定频道。
// @Tags feed
// @Produce json
// @Param channel_id path string true "频道 UUID"
// @Success 200 {object} SubscriptionStatusResponse
// @Failure 400 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscribe/channel/{channel_id}/status [get]
func CheckChannelSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		channelIDStr := c.Param("channel_id")
		channelID, err := uuid.Parse(channelIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel ID"})
			return
		}

		feedSource, _, found, err := resolveChannelSubscriptionFeedSource(db, channelID, false)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve subscription source"})
			return
		}
		if !found {
			c.JSON(http.StatusOK, gin.H{"subscribed": false})
			return
		}

		// Check subscription
		var sub model.Subscription
		err = db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).First(&sub).Error
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"subscribed": false})
			return
		}

		c.JSON(http.StatusOK, gin.H{"subscribed": true, "subscription": sub})
	}
}

// CheckCollectionSubscription checks if the current user is subscribed to a collection
// CheckCollectionSubscription godoc
// @Summary 查询合集订阅状态
// @Description 返回当前用户是否已订阅指定合集。
// @Tags feed
// @Produce json
// @Param collection_id path string true "合集 UUID"
// @Success 200 {object} SubscriptionStatusResponse
// @Failure 400 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscribe/collection/{collection_id}/status [get]
func CheckCollectionSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		collectionIDStr := c.Param("collection_id")
		_, err := uuid.Parse(collectionIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid collection ID"})
			return
		}

		// Find FeedSource for this collection
		var feedSource model.FeedSource
		hashStr := fmt.Sprintf("internal_collection:%s", collectionIDStr)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashStr)))

		if err := db.Where("hash = ?", hash).First(&feedSource).Error; err != nil {
			c.JSON(http.StatusOK, gin.H{"subscribed": false})
			return
		}

		// Check subscription
		var sub model.Subscription
		err = db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).First(&sub).Error
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"subscribed": false})
			return
		}

		c.JSON(http.StatusOK, gin.H{"subscribed": true, "subscription": sub})
	}
}
