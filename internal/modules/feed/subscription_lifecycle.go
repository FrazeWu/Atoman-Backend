package feed

import (
	"net/http"
	"time"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type SubscriptionPauseInput struct {
	Paused bool `json:"paused"`
}

type SubscriptionOrderInput struct {
	IDs []uuid.UUID `json:"ids" binding:"required"`
}

// SetSubscriptionPaused godoc
// @Summary 暂停或恢复订阅
// @Description 暂停仅影响当前用户的时间线与手动刷新，恢复后不补回暂停期间内容。
// @Tags feed
// @Accept json
// @Produce json
// @Param id path string true "订阅 UUID"
// @Param input body SubscriptionPauseInput true "暂停状态"
// @Success 200 {object} SubscriptionResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/{id}/pause [put]
func SetSubscriptionPaused(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		var input SubscriptionPauseInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}

		var subscription model.Subscription
		if err := db.Where("id = ? AND user_id = ?", c.Param("id"), userID).First(&subscription).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load subscription"})
			return
		}

		updates := map[string]any{"is_paused": input.Paused}
		if !input.Paused {
			now := time.Now().UTC()
			updates["resumed_after"] = now
		}
		if err := db.Model(&subscription).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update subscription"})
			return
		}
		if err := db.Preload("FeedSource").Preload("SubscriptionGroup").First(&subscription, "id = ?", subscription.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reload subscription"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": subscription, "message": "ok"})
	}
}

func nextSubscriptionPosition(db *gorm.DB, userID uuid.UUID, groupID *uuid.UUID) int {
	query := db.Model(&model.Subscription{}).Where("user_id = ?", userID)
	if groupID == nil {
		query = query.Where("subscription_group_id IS NULL")
	} else {
		query = query.Where("subscription_group_id = ?", *groupID)
	}
	var position int
	query.Select("COALESCE(MAX(position), -1)").Scan(&position)
	return position + 1
}

func updateOrderedPositions(tx *gorm.DB, userID uuid.UUID, ids []uuid.UUID, groupID *uuid.UUID) error {
	unique := uniqueSubscriptionIDs(ids)
	if len(unique) == 0 || len(unique) != len(ids) {
		return gorm.ErrInvalidData
	}
	query := tx.Model(&model.Subscription{}).Where("user_id = ? AND id IN ?", userID, unique)
	if groupID == nil {
		query = query.Where("subscription_group_id IS NULL")
	} else {
		query = query.Where("subscription_group_id = ?", *groupID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(unique)) {
		return gorm.ErrRecordNotFound
	}
	for position, id := range unique {
		if err := tx.Model(&model.Subscription{}).Where("id = ? AND user_id = ?", id, userID).Update("position", position).Error; err != nil {
			return err
		}
	}
	return nil
}

// ReorderSubscriptionGroups godoc
// @Summary 重排订阅分组
// @Tags feed
// @Accept json
// @Produce json
// @Param input body SubscriptionOrderInput true "完整分组 ID 顺序"
// @Success 200 {object} MessageResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/groups/order [put]
func ReorderSubscriptionGroups(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		var input SubscriptionOrderInput
		if err := c.ShouldBindJSON(&input); err != nil || len(input.IDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ids are required"})
			return
		}
		if len(uniqueSubscriptionIDs(input.IDs)) != len(input.IDs) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ids must be unique"})
			return
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			var count int64
			if err := tx.Model(&model.SubscriptionGroup{}).Where("user_id = ? AND id IN ?", userID, input.IDs).Count(&count).Error; err != nil {
				return err
			}
			if count != int64(len(input.IDs)) {
				return gorm.ErrRecordNotFound
			}
			for position, id := range input.IDs {
				if err := tx.Model(&model.SubscriptionGroup{}).Where("id = ? AND user_id = ?", id, userID).Update("position", position).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			status := http.StatusInternalServerError
			if err == gorm.ErrRecordNotFound {
				status = http.StatusNotFound
			}
			c.JSON(status, gin.H{"error": "failed to reorder groups"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// ReorderSubscriptions godoc
// @Summary 重排分组内订阅源
// @Tags feed
// @Accept json
// @Produce json
// @Param id path string true "分组 UUID"
// @Param input body SubscriptionOrderInput true "完整订阅 ID 顺序"
// @Success 200 {object} MessageResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/groups/{id}/subscriptions/order [put]
func ReorderSubscriptions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		groupID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid group id"})
			return
		}
		var input SubscriptionOrderInput
		if err := c.ShouldBindJSON(&input); err != nil || len(input.IDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ids are required"})
			return
		}
		err = db.Transaction(func(tx *gorm.DB) error {
			var groupCount int64
			if err := tx.Model(&model.SubscriptionGroup{}).Where("id = ? AND user_id = ?", groupID, userID).Count(&groupCount).Error; err != nil {
				return err
			}
			if groupCount != 1 {
				return gorm.ErrRecordNotFound
			}
			return updateOrderedPositions(tx, userID, input.IDs, &groupID)
		})
		if err != nil {
			status := http.StatusInternalServerError
			if err == gorm.ErrRecordNotFound {
				status = http.StatusNotFound
			}
			if err == gorm.ErrInvalidData {
				status = http.StatusBadRequest
			}
			c.JSON(status, gin.H{"error": "failed to reorder subscriptions"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}
