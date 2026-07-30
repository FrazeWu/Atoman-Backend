package feed

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ToggleStar godoc
// @Summary 切换条目星标
// @Description 为 feed item 添加或取消星标。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body StarToggleInput true "星标输入"
// @Success 200 {object} StarToggleResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/timeline/star [post]
func ToggleStar(db *gorm.DB) gin.HandlerFunc {
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

		var existing model.FeedItemStar
		err := db.Where("user_id = ? AND feed_item_id = ?", userID, input.FeedItemID).First(&existing).Error

		if err == nil {
			if err := db.Delete(&existing).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove star"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"starred": false, "message": "Star removed"})
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			star := model.FeedItemStar{
				UserID:     userID,
				FeedItemID: input.FeedItemID,
				StarredAt:  time.Now(),
			}
			if err := db.Create(&star).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add star"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"starred": true, "message": "Item starred"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		}
	}
}

func GetFeedStarGroups(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var groups []model.FeedStarGroup
		if err := db.Where("user_id = ?", userID).Order("created_at ASC").Find(&groups).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch star groups"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": groups})
	}
}

func CreateFeedStarGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var input struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}

		name := strings.TrimSpace(input.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
			return
		}

		group := model.FeedStarGroup{UserID: userID, Name: name}
		if err := db.Create(&group).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create star group"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": group, "message": "ok"})
	}
}

func UpdateFeedStarGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid star group id"})
			return
		}

		var input struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}

		name := strings.TrimSpace(input.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
			return
		}

		var group model.FeedStarGroup
		if err := db.Where("id = ? AND user_id = ?", id, userID).First(&group).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Star group not found"})
			return
		}

		if err := db.Model(&group).Update("name", name).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update star group"})
			return
		}
		group.Name = name

		c.JSON(http.StatusOK, gin.H{"data": group, "message": "ok"})
	}
}

func DeleteFeedStarGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid star group id"})
			return
		}

		var group model.FeedStarGroup
		if err := db.Where("id = ? AND user_id = ?", id, userID).First(&group).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Star group not found"})
			return
		}

		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.FeedItemStar{}).
				Where("user_id = ? AND group_id = ?", userID, id).
				Update("group_id", nil).Error; err != nil {
				return err
			}
			return tx.Delete(&group).Error
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete star group"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

func SetFeedStarGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		feedItemID, err := uuid.Parse(c.Param("feedItemId"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid feed item id"})
			return
		}

		var input struct {
			GroupID *uuid.UUID `json:"group_id"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}

		if input.GroupID != nil {
			var group model.FeedStarGroup
			if err := db.Where("id = ? AND user_id = ?", *input.GroupID, userID).First(&group).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Star group not found"})
				return
			}
		}

		result := db.Model(&model.FeedItemStar{}).
			Where("user_id = ? AND feed_item_id = ?", userID, feedItemID).
			Update("group_id", input.GroupID)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update star group"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "Star not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// GetStarredItems godoc
// @Summary 获取星标条目
// @Description 分页返回当前用户已星标的 feed item。
// @Tags feed
// @Produce json
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Success 200 {object} StarredItemsResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/stars [get]
func GetStarredItems(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if page < 1 {
			page = 1
		}
		if limit < 1 {
			limit = 20
		} else if limit > 500 {
			limit = 500
		}
		offset := (page - 1) * limit

		starQuery := db.Where("user_id = ?", userID)
		if groupIDParam := strings.TrimSpace(c.Query("group_id")); groupIDParam != "" {
			groupID, err := uuid.Parse(groupIDParam)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid group_id"})
				return
			}
			starQuery = starQuery.Where("group_id = ?", groupID)
		}

		var total int64
		if err := starQuery.Model(&model.FeedItemStar{}).Count(&total).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count starred items"})
			return
		}

		var stars []model.FeedItemStar
		if err := starQuery.Order("starred_at DESC").Offset(offset).Limit(limit).Find(&stars).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch starred items"})
			return
		}

		feedItemIDs := make([]uuid.UUID, len(stars))
		for i, star := range stars {
			feedItemIDs[i] = star.FeedItemID
		}

		var feedItems []model.FeedItem
		if err := db.Where("id IN ?", feedItemIDs).Find(&feedItems).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch feed items"})
			return
		}

		type FeedItemWithSource struct {
			model.FeedItem
			GroupID        *uuid.UUID `json:"group_id"`
			SourceTitle    string     `json:"source_title"`
			SourceSiteURL  string     `json:"source_site_url"`
			SourceImageURL string     `json:"source_image_url"`
		}

		starGroups := make(map[uuid.UUID]*uuid.UUID, len(stars))
		for _, star := range stars {
			starGroups[star.FeedItemID] = star.GroupID
		}

		feedItemsByID := make(map[uuid.UUID]model.FeedItem, len(feedItems))
		for _, item := range feedItems {
			feedItemsByID[item.ID] = item
		}

		result := make([]FeedItemWithSource, 0, len(feedItems))
		for _, star := range stars {
			item, ok := feedItemsByID[star.FeedItemID]
			if !ok {
				continue
			}

			var source model.FeedSource
			if err := db.First(&source, item.FeedSourceID).Error; err == nil {
				result = append(result, FeedItemWithSource{
					FeedItem:       item,
					GroupID:        starGroups[item.ID],
					SourceTitle:    source.Title,
					SourceSiteURL:  source.RssURL, // Use RSS URL as site URL
					SourceImageURL: "",            // No image field in FeedSource
				})
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"items": result,
			"page":  page,
			"total": total,
		})
	}
}
