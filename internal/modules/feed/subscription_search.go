package feed

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"atoman/internal/model"
	"atoman/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SearchSubscriptions searches user's subscriptions by title or feed source title
// SearchSubscriptions godoc
// @Summary 搜索订阅
// @Description 按标题搜索当前用户的订阅。
// @Tags feed
// @Produce json
// @Param q query string true "搜索关键字"
// @Param limit query int false "返回数量"
// @Success 200 {object} SearchSubscriptionsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/search [get]
func SearchSubscriptions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		query := c.Query("q")
		if query == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Query parameter 'q' is required"})
			return
		}

		limitStr := c.DefaultQuery("limit", "20")
		limit, err := strconv.Atoi(limitStr)
		if err != nil || limit < 1 || limit > 100 {
			limit = 20
		}

		var subscriptions []model.Subscription
		err = db.Where("user_id = ?", userID).
			Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
			Where("subscriptions.title ILIKE ? OR feed_sources.title ILIKE ?", "%"+query+"%", "%"+query+"%").
			Limit(limit).
			Preload("FeedSource").
			Find(&subscriptions).Error

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to search subscriptions"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data":  subscriptions,
			"count": len(subscriptions),
		})
	}
}

// GetFeedItem retrieves a single feed item by ID
// GetFeedItem godoc
// @Summary 获取单个 feed 条目
// @Description 返回单个 feed item 详情。
// @Tags feed
// @Produce json
// @Param id path string true "Feed item UUID"
// @Success 200 {object} FeedItemResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/items/{id} [get]
func GetFeedItem(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		itemID := c.Param("id")
		id, err := uuid.Parse(itemID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid item ID"})
			return
		}

		var item model.FeedItem
		err = db.Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
			Preload("FeedSource").
			First(&item, "feed_items.id = ? AND feed_sources.hidden = ? AND feed_sources.deleted_at IS NULL", id, false).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "Feed item not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch feed item"})
			return
		}

		contentHTML := strings.TrimSpace(item.ReaderHTML)
		contentSource := strings.TrimSpace(item.ReaderSource)
		if contentHTML == "" && item.FullTextStatus == service.FullTextStatusSuccess {
			contentHTML = strings.TrimSpace(item.FullTextHTML)
			contentSource = service.ReaderSourcePage
		}
		if contentHTML == "" {
			contentHTML = strings.TrimSpace(item.Summary)
			contentSource = service.ReaderSourceSummary
		}

		c.JSON(http.StatusOK, gin.H{
			"data": FeedItemDetailResponse{
				ID:            item.ID,
				Title:         item.Title,
				Summary:       item.Summary,
				Link:          item.Link,
				Author:        item.Author,
				PublishedAt:   item.PublishedAt,
				ImageURL:      item.ImageURL,
				EnclosureURL:  item.EnclosureURL,
				EnclosureType: item.EnclosureType,
				Duration:      item.Duration,
				ContentHTML:   contentHTML,
				ContentSource: contentSource,
				FeedSource:    item.FeedSource,
				FeedItem:      &item,
			},
		})
	}
}
