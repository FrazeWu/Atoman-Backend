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
// @Description 分页返回公开文章与 RSS 条目，支持搜索、来源类型、语言和排序筛选。
// @Tags feed
// @Produce json
// @Param sort query string false "排序方式" Enums(recent,random,popular)
// @Param q query string false "搜索标题、摘要和来源"
// @Param category query string false "来源类型" Enums(blog,news,social,video,forum,podcast,all)
// @Param language query string false "语言代码或 all"
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Success 200 {object} TimelineResponse
// @Failure 400 {object} ErrorResponse
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
