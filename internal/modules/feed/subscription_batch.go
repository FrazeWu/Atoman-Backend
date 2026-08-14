package feed

import (
	"net/http"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxBatchSubscriptions = 500

type BatchSubscriptionUpdateInput struct {
	IDs                []uuid.UUID `json:"ids" binding:"required"`
	GroupID            *uuid.UUID  `json:"group_id"`
	IsMuted            *bool       `json:"is_muted"`
	AutoMarkRead       *bool       `json:"auto_mark_read"`
	AutoAddReadingList *bool       `json:"auto_add_reading_list"`
}

type BatchSubscriptionDeleteInput struct {
	IDs []uuid.UUID `json:"ids" binding:"required"`
}

func uniqueSubscriptionIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	unique := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}

func validateBatchSubscriptionIDs(c *gin.Context, ids []uuid.UUID) ([]uuid.UUID, bool) {
	unique := uniqueSubscriptionIDs(ids)
	if len(unique) == 0 || len(unique) > maxBatchSubscriptions {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids must contain between 1 and 500 subscriptions"})
		return nil, false
	}
	return unique, true
}

// BatchUpdateSubscriptions godoc
// @Summary 批量更新订阅
// @Description 批量更新当前用户订阅的分组与处理策略；任一订阅无效时整体失败。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body BatchSubscriptionUpdateInput true "批量更新参数"
// @Success 200 {object} object
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/batch-update [put]
func BatchUpdateSubscriptions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		var input BatchSubscriptionUpdateInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}
		ids, ok := validateBatchSubscriptionIDs(c, input.IDs)
		if !ok {
			return
		}

		updates := map[string]any{}
		if input.GroupID != nil {
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
		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
			return
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			var count int64
			if err := tx.Model(&model.Subscription{}).Where("user_id = ? AND id IN ?", userID, ids).Count(&count).Error; err != nil {
				return err
			}
			if count != int64(len(ids)) {
				return gorm.ErrRecordNotFound
			}
			if input.GroupID != nil {
				var groupCount int64
				if err := tx.Model(&model.SubscriptionGroup{}).Where("user_id = ? AND id = ?", userID, *input.GroupID).Count(&groupCount).Error; err != nil {
					return err
				}
				if groupCount != 1 {
					return gorm.ErrRecordNotFound
				}
			}
			return tx.Model(&model.Subscription{}).Where("user_id = ? AND id IN ?", userID, ids).Updates(updates).Error
		})
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "subscription or group not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update subscriptions"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"updated": len(ids)}})
	}
}

// BatchDeleteSubscriptions godoc
// @Summary 批量取消订阅
// @Description 批量删除当前用户订阅；任一订阅无效时整体失败。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body BatchSubscriptionDeleteInput true "批量删除参数"
// @Success 200 {object} object
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/batch-delete [post]
func BatchDeleteSubscriptions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		var input BatchSubscriptionDeleteInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}
		ids, ok := validateBatchSubscriptionIDs(c, input.IDs)
		if !ok {
			return
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			var count int64
			if err := tx.Model(&model.Subscription{}).Where("user_id = ? AND id IN ?", userID, ids).Count(&count).Error; err != nil {
				return err
			}
			if count != int64(len(ids)) {
				return gorm.ErrRecordNotFound
			}
			return tx.Where("user_id = ? AND id IN ?", userID, ids).Delete(&model.Subscription{}).Error
		})
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete subscriptions"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"deleted": len(ids)}})
	}
}
