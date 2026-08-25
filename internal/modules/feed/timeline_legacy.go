package feed

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type TimelineItem struct {
	Type        string          `json:"type"`
	Post        *model.Post     `json:"post,omitempty"`
	FeedItem    *model.FeedItem `json:"feed_item,omitempty"`
	PublishedAt time.Time       `json:"published_at"`
	IsRead      bool            `json:"is_read"`
}

type FeedStatsPoint struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type FeedSourceStat struct {
	FeedSourceID uuid.UUID `json:"feed_source_id"`
	Title        string    `json:"title"`
	Count        int       `json:"count"`
}

type FeedStatsData struct {
	Period          string           `json:"period"`
	TotalRead       int              `json:"total_read"`
	Points          []FeedStatsPoint `json:"points"`
	SourceBreakdown []FeedSourceStat `json:"source_breakdown"`
}

type feedReadEvent struct {
	ReadAt       time.Time `gorm:"column:read_at"`
	FeedSourceID uuid.UUID `gorm:"column:feed_source_id"`
	SourceTitle  string    `gorm:"column:source_title"`
}

// GetTimeline is kept for legacy compatibility tests.
// @Summary 获取订阅时间线
// @Description 聚合博客文章与外部 RSS 条目时间线，支持分页和筛选。
// @Tags feed
// @Produce json
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Param content_type query string false "内容类型" Enums(blog)
// @Param source_type query string false "源类型"
// @Param source_id query string false "订阅 UUID"
// @Param group_id query string false "分组 UUID"
// @Param is_read query string false "是否已读" Enums(true,false)
// @Param hide_duplicates query bool false "隐藏重复项"
// @Success 200 {object} TimelineResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/timeline [get]
func GetTimeline(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if limit > 100 {
			limit = 100
		}
		offset := (page - 1) * limit

		sourceType := c.Query("source_type")
		sourceID := c.Query("source_id")
		groupID := c.Query("group_id")
		isReadFilter := c.Query("is_read") // "true", "false", or "" for all
		unreadOnly := strings.EqualFold(c.Query("unread_only"), "true")
		hideDuplicates := c.Query("hide_duplicates") == "true"

		var userSubscriptions []model.Subscription
		query := db.Where("subscriptions.user_id = ? AND subscriptions.is_paused = ?", userID, false)

		if sourceType != "" {
			query = query.Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
				Where("feed_sources.source_type = ?", sourceType)
		}
		if sourceID != "" {
			query = query.Where("subscriptions.id = ?", sourceID)
		}
		if groupID != "" {
			query = query.Where("subscriptions.subscription_group_id = ?", groupID)
		}

		if err := query.Preload("FeedSource").Find(&userSubscriptions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch subscriptions"})
			return
		}

		if len(userSubscriptions) == 0 {
			c.JSON(http.StatusOK, gin.H{"data": []TimelineItem{}, "total": 0, "page": page, "limit": limit, "message": "ok"})
			return
		}

		var userIDs []uuid.UUID
		var channelIDs []uuid.UUID
		var collectionIDs []uuid.UUID
		var feedSourceIDs []uuid.UUID
		feedSourceVisibleAfter := make(map[uuid.UUID]*time.Time)

		for _, sub := range userSubscriptions {
			fs := sub.FeedSource
			if fs == nil {
				continue
			}
			switch fs.SourceType {
			case "internal_user":
				if fs.SourceID != nil {
					userIDs = append(userIDs, *fs.SourceID)
				}
			case "internal_channel":
				if fs.SourceID != nil {
					channelIDs = append(channelIDs, *fs.SourceID)
				}
			case "internal_collection":
				if fs.SourceID != nil {
					collectionIDs = append(collectionIDs, *fs.SourceID)
				}
			case "external_rss":
				feedSourceIDs = append(feedSourceIDs, fs.ID)
				feedSourceVisibleAfter[fs.ID] = sub.ResumedAfter
			}
		}

		var posts []model.Post
		var orConditions []string
		var orArgs []interface{}

		if len(userIDs) > 0 {
			orConditions = append(orConditions, "user_id IN ?")
			orArgs = append(orArgs, userIDs)
		}

		if len(channelIDs) > 0 {
			orConditions = append(orConditions, "channel_id IN ?")
			orArgs = append(orArgs, channelIDs)

			var channelCollections []model.Collection
			db.Where("channel_id IN ?", channelIDs).Find(&channelCollections)
			for _, col := range channelCollections {
				collectionIDs = append(collectionIDs, col.ID)
			}
		}

		if len(collectionIDs) > 0 {
			var postCollections []model.PostCollection
			db.Where("collection_id IN ?", collectionIDs).Find(&postCollections)
			var postIDs []uuid.UUID
			for _, pc := range postCollections {
				postIDs = append(postIDs, pc.PostID)
			}
			if len(postIDs) > 0 {
				orConditions = append(orConditions, "id IN ?")
				orArgs = append(orArgs, postIDs)
			}
		}

		if len(orConditions) > 0 {
			combined := "(" + strings.Join(orConditions, " OR ") + ")"
			db.Preload("User").Where("status = ?", "published").Where(combined, orArgs...).Find(&posts)
		}

		var feedItems []model.FeedItem
		if len(feedSourceIDs) > 0 {
			db.Preload("FeedSource").
				Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
				Where("feed_items.feed_source_id IN ?", feedSourceIDs).
				Where("feed_sources.hidden = ?", false).
				Order("feed_items.published_at DESC").
				Find(&feedItems)
			service.AnnotateDuplicateFeedItems(feedItems)
			visibleFeedItems := feedItems[:0]
			for _, item := range feedItems {
				visibleAfter := feedSourceVisibleAfter[item.FeedSourceID]
				if visibleAfter != nil && item.PublishedAt.Before(*visibleAfter) {
					continue
				}
				visibleFeedItems = append(visibleFeedItems, item)
			}
			feedItems = visibleFeedItems
		}

		var readFeedItemIDs map[uuid.UUID]bool
		if len(feedItems) > 0 {
			var feedItemIDs []uuid.UUID
			for _, fi := range feedItems {
				feedItemIDs = append(feedItemIDs, fi.ID)
			}
			var reads []model.FeedItemRead
			db.Where("user_id = ? AND feed_item_id IN ?", userID, feedItemIDs).Find(&reads)
			readFeedItemIDs = make(map[uuid.UUID]bool, len(reads))
			for _, r := range reads {
				readFeedItemIDs[r.FeedItemID] = true
			}
		}

		var timeline []TimelineItem

		for i := range posts {
			timeline = append(timeline, TimelineItem{
				Type:        "post",
				Post:        &posts[i],
				PublishedAt: posts[i].CreatedAt,
				IsRead:      false,
			})
		}

		for i := range feedItems {
			timeline = append(timeline, TimelineItem{
				Type:        "feed_item",
				FeedItem:    &feedItems[i],
				PublishedAt: feedItems[i].PublishedAt,
				IsRead:      readFeedItemIDs[feedItems[i].ID],
			})
		}

		sort.Slice(timeline, func(i, j int) bool {
			return timeline[i].PublishedAt.After(timeline[j].PublishedAt)
		})

		if isReadFilter == "true" || isReadFilter == "false" {
			want := isReadFilter == "true"
			filtered := timeline[:0]
			for _, item := range timeline {
				if item.IsRead == want {
					filtered = append(filtered, item)
				}
			}
			timeline = filtered
		}

		if unreadOnly {
			filtered := timeline[:0]
			for _, item := range timeline {
				if !item.IsRead {
					filtered = append(filtered, item)
				}
			}
			timeline = filtered
		}

		if hideDuplicates {
			filtered := timeline[:0]
			for _, item := range timeline {
				if item.Type == "feed_item" && item.FeedItem != nil && item.FeedItem.IsDuplicate {
					continue
				}
				filtered = append(filtered, item)
			}
			timeline = filtered
		}

		total := len(timeline)
		start := offset
		if start > total {
			start = total
		}
		end := start + limit
		if end > total {
			end = total
		}
		paged := timeline[start:end]

		c.JSON(http.StatusOK, gin.H{
			"data":    paged,
			"total":   total,
			"page":    page,
			"limit":   limit,
			"message": "ok",
		})
	}
}

// GetFeedStats godoc
// @Summary 获取订阅统计
// @Description 返回阅读统计数据和来源分布。
// @Tags feed
// @Produce json
// @Param period query string false "统计周期" Enums(day,week,month)
// @Success 200 {object} FeedStatsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/stats [get]
func GetFeedStats(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		period := strings.ToLower(c.DefaultQuery("period", "day"))

		points, pointIndex, rangeStart, err := buildFeedStatsBuckets(period, time.Now())
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid period"})
			return
		}

		var events []feedReadEvent
		if err := db.Table("feed_item_reads AS fir").
			Select("fir.read_at, fi.feed_source_id, COALESCE(NULLIF(subscriptions.title, ''), fs.title, '未命名源') AS source_title").
			Joins("JOIN feed_items fi ON fi.id = fir.feed_item_id").
			Joins("LEFT JOIN feed_sources fs ON fs.id = fi.feed_source_id").
			Joins("LEFT JOIN subscriptions ON subscriptions.feed_source_id = fi.feed_source_id AND subscriptions.user_id = ?", userID).
			Where("fir.user_id = ? AND fir.read_at >= ?", userID, rangeStart).
			Order("fir.read_at ASC").
			Scan(&events).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch feed stats"})
			return
		}

		totalRead := 0
		sourceCounts := make(map[uuid.UUID]*FeedSourceStat)

		for _, event := range events {
			bucketKey := feedStatsBucketKey(period, event.ReadAt)
			if pointPos, ok := pointIndex[bucketKey]; ok {
				points[pointPos].Count++
				totalRead++
			}

			stat, ok := sourceCounts[event.FeedSourceID]
			if !ok {
				stat = &FeedSourceStat{
					FeedSourceID: event.FeedSourceID,
					Title:        event.SourceTitle,
				}
				sourceCounts[event.FeedSourceID] = stat
			}
			stat.Count++
		}

		sourceBreakdown := make([]FeedSourceStat, 0, len(sourceCounts))
		for _, stat := range sourceCounts {
			sourceBreakdown = append(sourceBreakdown, *stat)
		}

		sort.Slice(sourceBreakdown, func(i, j int) bool {
			if sourceBreakdown[i].Count == sourceBreakdown[j].Count {
				return sourceBreakdown[i].Title < sourceBreakdown[j].Title
			}
			return sourceBreakdown[i].Count > sourceBreakdown[j].Count
		})

		if len(sourceBreakdown) > 10 {
			sourceBreakdown = sourceBreakdown[:10]
		}

		c.JSON(http.StatusOK, gin.H{
			"data": FeedStatsData{
				Period:          period,
				TotalRead:       totalRead,
				Points:          points,
				SourceBreakdown: sourceBreakdown,
			},
			"message": "ok",
		})
	}
}

func buildFeedStatsBuckets(period string, now time.Time) ([]FeedStatsPoint, map[string]int, time.Time, error) {
	now = now.Local()

	switch period {
	case "day":
		const bucketCount = 7
		start := startOfDay(now).AddDate(0, 0, -(bucketCount - 1))
		points, pointIndex, rangeStart := feedStatsPoints(bucketCount, start, func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }, func(t time.Time) string {
			return t.Format("01-02")
		}, func(t time.Time) string {
			return startOfDay(t).Format("2006-01-02")
		})
		return points, pointIndex, rangeStart, nil
	case "week":
		const bucketCount = 8
		start := startOfISOWeek(now).AddDate(0, 0, -7*(bucketCount-1))
		points, pointIndex, rangeStart := feedStatsPoints(bucketCount, start, func(t time.Time) time.Time { return t.AddDate(0, 0, 7) }, func(t time.Time) string {
			return t.Format("01-02")
		}, func(t time.Time) string {
			return startOfISOWeek(t).Format("2006-01-02")
		})
		return points, pointIndex, rangeStart, nil
	case "month":
		const bucketCount = 6
		start := startOfMonth(now).AddDate(0, -(bucketCount - 1), 0)
		points, pointIndex, rangeStart := feedStatsPoints(bucketCount, start, func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }, func(t time.Time) string {
			return t.Format("2006-01")
		}, func(t time.Time) string {
			return startOfMonth(t).Format("2006-01")
		})
		return points, pointIndex, rangeStart, nil
	default:
		return nil, nil, time.Time{}, fmt.Errorf("unsupported period: %s", period)
	}
}

func feedStatsPoints(
	bucketCount int,
	start time.Time,
	next func(time.Time) time.Time,
	labelFor func(time.Time) string,
	keyFor func(time.Time) string,
) ([]FeedStatsPoint, map[string]int, time.Time) {
	points := make([]FeedStatsPoint, 0, bucketCount)
	pointIndex := make(map[string]int, bucketCount)
	current := start

	for i := 0; i < bucketCount; i++ {
		key := keyFor(current)
		pointIndex[key] = i
		points = append(points, FeedStatsPoint{Label: labelFor(current), Count: 0})
		current = next(current)
	}

	return points, pointIndex, start
}

func feedStatsBucketKey(period string, t time.Time) string {
	switch period {
	case "week":
		return startOfISOWeek(t).Format("2006-01-02")
	case "month":
		return startOfMonth(t).Format("2006-01")
	default:
		return startOfDay(t).Format("2006-01-02")
	}
}

func startOfDay(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, t.Location())
}

func startOfISOWeek(t time.Time) time.Time {
	start := startOfDay(t)
	weekday := int(start.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	return start.AddDate(0, 0, -(weekday - 1))
}

func startOfMonth(t time.Time) time.Time {
	year, month, _ := t.Date()
	return time.Date(year, month, 1, 0, 0, 0, 0, t.Location())
}

type MarkReadInput struct {
	FeedItemIDs []uuid.UUID `json:"feed_item_ids" binding:"required"`
}

// MarkItemsRead godoc
// @Summary 标记条目已读
// @Description 批量将指定 feed item 标记为已读。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body MarkReadInput true "已读输入"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/timeline/mark-read [post]
func MarkItemsRead(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input MarkReadInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		now := time.Now()

		for _, itemID := range input.FeedItemIDs {
			read := model.FeedItemRead{
				UserID:     userID,
				FeedItemID: itemID,
				ReadAt:     now,
			}
			db.Where("user_id = ? AND feed_item_id = ?", userID, itemID).
				FirstOrCreate(&read)
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// MarkAllRead godoc
// @Summary 全部标记已读
// @Description 将当前用户外部 RSS 条目全部标记为已读。
// @Tags feed
// @Produce json
// @Success 200 {object} MessageResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/timeline/mark-all-read [post]
func MarkAllRead(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var userSubscriptions []model.Subscription
		db.Where("user_id = ?", userID).Preload("FeedSource").Find(&userSubscriptions)

		var feedSourceIDs []uuid.UUID
		for _, sub := range userSubscriptions {
			if sub.FeedSource != nil && sub.FeedSource.SourceType == "external_rss" {
				feedSourceIDs = append(feedSourceIDs, sub.FeedSource.ID)
			}
		}

		if len(feedSourceIDs) == 0 {
			c.JSON(http.StatusOK, gin.H{"message": "ok"})
			return
		}

		var unreadItems []model.FeedItem
		db.Where("feed_source_id IN ?", feedSourceIDs).Find(&unreadItems)

		now := time.Now()
		for _, item := range unreadItems {
			read := model.FeedItemRead{
				UserID:     userID,
				FeedItemID: item.ID,
				ReadAt:     now,
			}
			db.Where("user_id = ? AND feed_item_id = ?", userID, item.ID).
				FirstOrCreate(&read)
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// MarkAllUnread godoc
// @Summary 全部标记未读
// @Description 将当前用户外部 RSS 条目全部标记为未读。
// @Tags feed
// @Produce json
// @Success 200 {object} MessageResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/timeline/mark-all-unread [post]
func MarkAllUnread(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var userSubscriptions []model.Subscription
		db.Where("user_id = ?", userID).Preload("FeedSource").Find(&userSubscriptions)

		var feedSourceIDs []uuid.UUID
		for _, sub := range userSubscriptions {
			if sub.FeedSource != nil && sub.FeedSource.SourceType == "external_rss" {
				feedSourceIDs = append(feedSourceIDs, sub.FeedSource.ID)
			}
		}

		if len(feedSourceIDs) == 0 {
			c.JSON(http.StatusOK, gin.H{"message": "ok"})
			return
		}

		var feedItems []model.FeedItem
		db.Where("feed_source_id IN ?", feedSourceIDs).Find(&feedItems)

		var itemIDs []uuid.UUID
		for _, item := range feedItems {
			itemIDs = append(itemIDs, item.ID)
		}

		if len(itemIDs) > 0 {
			db.Where("user_id = ? AND feed_item_id IN ?", userID, itemIDs).Delete(&model.FeedItemRead{})
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}
