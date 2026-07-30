package feed

import (
	"net/http"
	"strconv"
	"strings"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetExploreFeed godoc
// @Summary 探索订阅条目
// @Description 分页返回推荐条目，支持按最近 (recent)、随机 (random) 或热门 (popular) 排序。
// @Tags feed
// @Produce json
// @Param sort query string false "排序方式" Enums(recent,random,popular)
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Success 200 {object} TimelineResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/explore [get]
func GetExploreFeed(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		sort := strings.TrimSpace(strings.ToLower(c.DefaultQuery("sort", "recent")))
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if limit > 100 {
			limit = 100
		}
		offset := (page - 1) * limit

		var items []model.FeedItem
		query := db.Preload("FeedSource")

		switch sort {
		case "popular":
			query = query.Select("feed_items.*, (SELECT COUNT(*) FROM feed_item_stars WHERE feed_item_stars.feed_item_id = feed_items.id) as star_count").
				Order("star_count DESC, published_at DESC, feed_items.id DESC")
		case "random":
			query = query.Order("RANDOM()")
		default:
			query = query.Order("published_at DESC, feed_items.id DESC")
		}

		if err := query.Offset(offset).Limit(limit).Find(&items).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch explore feed"})
			return
		}

		// Convert to TimelineItem format
		var data []any
		for _, item := range items {
			data = append(data, gin.H{
				"type":         "feed_item",
				"feed_item":    item,
				"published_at": item.PublishedAt,
			})
		}

		c.JSON(http.StatusOK, gin.H{"data": data, "message": "ok"})
	}
}
