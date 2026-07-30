package feed

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ToggleReadingListItem godoc
// @Summary 切换稍后读条目
// @Description 为 feed item 添加或移除稍后读标记。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body StarToggleInput true "稍后读输入"
// @Success 200 {object} SaveToggleResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
func ToggleReadingListItem(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var input struct {
			FeedItemID uuid.UUID `json:"feed_item_id" binding:"required"`
		}

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "feed_item_id is required"})
			return
		}

		var feedItem model.FeedItem
		if err := db.First(&feedItem, input.FeedItemID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Feed item not found"})
			return
		}

		var existing model.ReadingListItem
		err := db.Where("user_id = ? AND target_type = ? AND target_id = ?", userID, "feed_item", input.FeedItemID).First(&existing).Error

		if err == nil {
			if err := db.Delete(&existing).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove reading list item"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"saved": false})
			return
		}

		if !errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
			return
		}

		item := model.ReadingListItem{
			UserID:     userID,
			TargetType: "feed_item",
			TargetID:   input.FeedItemID,
			CreatedAt:  time.Now(),
		}
		if err := db.Create(&item).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add reading list item"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"saved": true})
	}
}

// GetReadingListItems godoc
// @Summary 获取稍后读列表
// @Description 分页返回当前用户的稍后读条目。
// @Tags feed
// @Produce json
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Success 200 {object} ReadingListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
func GetReadingListItems(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if page < 1 {
			page = 1
		}
		if limit < 1 || limit > 100 {
			limit = 20
		}
		offset := (page - 1) * limit

		var total int64
		db.Model(&model.ReadingListItem{}).Where("user_id = ?", userID).Count(&total)

		var listItems []model.ReadingListItem
		if err := db.Preload("FeedItem").Preload("FeedItem.FeedSource").Preload("Post").
			Where("user_id = ?", userID).
			Order("created_at DESC").
			Offset(offset).
			Limit(limit).
			Find(&listItems).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch reading list"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"items": listItems,
			"page":  page,
			"total": total,
		})
	}
}

func RemoveReadingListItem(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		feedItemID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid feed item id"})
			return
		}

		if err := db.Where("user_id = ? AND target_type = ? AND target_id = ?", userID, "feed_item", feedItemID).Delete(&model.ReadingListItem{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove reading list item"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"removed": true})
	}
}
