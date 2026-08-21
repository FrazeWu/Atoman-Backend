package feed

import (
	"net/http"
	"strconv"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func GetExploreSources(db *gorm.DB) gin.HandlerFunc {
	repo := NewRepo(db)
	return func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if page < 1 {
			page = 1
		}
		if limit < 1 {
			limit = 20
		}
		if limit > 100 {
			limit = 100
		}
		category := c.Query("category")
		query := c.Query("q")
		languageCode, err := parseRecommendationLanguage(c.Query("language"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid language"})
			return
		}
		offset := (page - 1) * limit

		rows, err := repo.ListExploreSources(limit, offset, category, query, languageCode)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch explore sources"})
			return
		}
		if userIDValue, ok := c.Get("user_id"); ok {
			if userID, valid := userIDValue.(uuid.UUID); valid && len(rows) > 0 {
				ids := make([]uuid.UUID, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				var subscriptions []model.Subscription
				db.Where("user_id = ? AND feed_source_id IN ?", userID, ids).Find(&subscriptions)
				subscribed := map[uuid.UUID]bool{}
				for _, subscription := range subscriptions {
					subscribed[subscription.FeedSourceID] = true
				}
				for index := range rows {
					rows[index].Subscribed = subscribed[rows[index].ID]
				}
			}
		}
		total, err := repo.CountExploreSources(category, query, languageCode)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count explore sources"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": rows,
			"meta": gin.H{
				"page":      page,
				"page_size": limit,
				"total":     total,
				"has_more":  int64(offset+len(rows)) < total,
			},
		})
	}
}
