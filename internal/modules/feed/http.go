package feed

import (
	"net/http"
	"strconv"
	"strings"

	"atoman/internal/middleware"
	studioapi "atoman/internal/modules/studio"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Handler struct {
	service *Service
}

type ReadingListInput struct {
	TargetType string    `json:"target_type" enums:"feed_item,post"`
	TargetID   uuid.UUID `json:"target_id"`
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.GET("/timeline", middleware.OptionalAuthMiddleware(), h.getSubscribedFeed)
	group.GET("/explore", middleware.OptionalAuthMiddleware(), h.getExploreFeed)
	group.GET("/explore/sources", GetExploreSources(service.db))
	group.GET("/recommend/themes", h.getRecommendationThemes)
	group.GET("/recommend/articles", h.getRecommendedArticles)
	group.GET("/recommend/channels", h.getRecommendedChannels)

	group.GET("/rss/:username", GetUserRSS(service.db))
	group.GET("/items/:id", GetFeedItem(service.db))

	protected := group.Group("")
	protected.Use(middleware.AuthMiddleware())
	{
		protected.POST("/timeline/mark-read", h.markRead)
		protected.POST("/timeline/mark-unread", h.markUnread)
		protected.POST("/timeline/mark-all-read", h.markAllRead)
		protected.POST("/timeline/mark-all-unread", h.markAllUnread)
		protected.POST("/timeline/star", h.toggleStar)
		protected.POST("/events/read", h.recordReadEvent)
		protected.GET("/reading-list", h.listReadingList)
		protected.POST("/reading-list", h.toggleReadingList)
		protected.DELETE("/reading-list/:target_type/:id", h.removeReadingListItem)
		protected.POST("/discover", DiscoverFeedCandidates())
		protected.POST("/sources/create-from-provider", CreateSubscriptionFromProvider(service.db))
		protected.GET("/subscriptions", GetSubscriptions(service.db))
		protected.POST("/subscriptions/resolve", ResolveSubscriptionInput(service.db))
		protected.POST("/subscriptions/auto-add", AutoAddSubscription(service.db))
		protected.POST("/subscriptions", CreateSubscription(service.db))
		protected.DELETE("/subscriptions/:id", DeleteSubscription(service.db))
		protected.PUT("/subscriptions/:id", UpdateSubscription(service.db))
		protected.GET("/stats", GetFeedStats(service.db))
		protected.GET("/groups", GetSubscriptionGroups(service.db))
		protected.POST("/groups", CreateSubscriptionGroup(service.db))
		protected.PUT("/groups/:id", UpdateSubscriptionGroup(service.db))
		protected.DELETE("/groups/:id", DeleteSubscriptionGroup(service.db))
		protected.PUT("/subscriptions/:id/group", SetSubscriptionGroup(service.db))
		protected.POST("/opml/import", ImportOPML(service.db))
		protected.GET("/opml/export", ExportOPML(service.db))
		protected.POST("/sources/opml/import", middleware.AdminMiddleware(service.db), ImportGlobalOPML(service.db))
		protected.GET("/sources/opml/export", middleware.AdminMiddleware(service.db), ExportGlobalOPML(service.db))
		protected.GET("/stars", GetStarredItems(service.db))
		protected.GET("/star-groups", GetFeedStarGroups(service.db))
		protected.POST("/star-groups", CreateFeedStarGroup(service.db))
		protected.PUT("/star-groups/:id", UpdateFeedStarGroup(service.db))
		protected.DELETE("/star-groups/:id", DeleteFeedStarGroup(service.db))
		protected.PUT("/stars/:feedItemId/group", SetFeedStarGroup(service.db))
		protected.GET("/subscriptions/search", SearchSubscriptions(service.db))
		protected.POST("/subscriptions/:id/health", CheckSubscriptionHealth(service.db))
		protected.POST("/subscriptions/health/check-all", CheckAllSubscriptionsHealth(service.db))
		protected.POST("/subscribe/channel/:channel_id", SubscribeChannel(service.db))
		protected.DELETE("/subscribe/channel/:channel_id", UnsubscribeChannel(service.db))
		protected.GET("/subscribe/channel/:channel_id/status", CheckChannelSubscription(service.db))
		protected.POST("/subscribe/collection/:collection_id", SubscribeCollection(service.db))
		protected.DELETE("/subscribe/collection/:collection_id", UnsubscribeCollection(service.db))
		protected.GET("/subscribe/collection/:collection_id/status", CheckCollectionSubscription(service.db))
	}
}

// getSubscribedFeed godoc
// @Summary 获取订阅时间线
// @Description 聚合文章与外部 RSS 条目，支持分页和筛选。
// @Tags feed
// @Produce json
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Param source_type query string false "源类型"
// @Param source_id query string false "订阅 UUID"
// @Param group_id query string false "分组 UUID"
// @Param is_read query string false "是否已读" Enums(true,false)
// @Param hide_duplicates query bool false "隐藏重复项"
// @Success 200 {object} TimelineListResponseDTO
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/timeline [get]
func (h *Handler) getSubscribedFeed(c *gin.Context) {
	user, _ := authctx.Current(c)
	query, err := queryFromContext(c)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if raw := c.Query("feed_source_id"); raw != "" {
		feedSourceID, err := uuid.Parse(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "feed source id must be a valid uuid"))
			return
		}
		items, total, err := h.service.GetPublicFeedBySourceID(feedSourceID, query)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		httpx.List(c, items, normalizedPageFromQuery(c), normalizedPageSizeFromQuery(c), total)
		return
	}
	items, total, err := h.service.GetSubscribedFeed(user, query)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, normalizedPageFromQuery(c), normalizedPageSizeFromQuery(c), total)
}

func (h *Handler) getExploreFeed(c *gin.Context) {
	user, _ := authctx.Current(c)
	query, err := queryFromContext(c)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	items, total, err := h.service.GetExploreFeed(user, query)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, normalizedPageFromQuery(c), normalizedPageSizeFromQuery(c), total)
}

func (h *Handler) getRecommendedArticles(c *gin.Context) {
	mode, err := parseRecommendationMode(c.Query("mode"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	category := c.Query("category")
	theme := c.Query("theme")
	page, pageSize := httpx.PageParams(c)
	items, total, err := h.service.RecommendArticlesByMode(mode, category, theme, page, pageSize)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, page, pageSize, total)
}

func (h *Handler) getRecommendedChannels(c *gin.Context) {
	mode, err := parseRecommendationMode(c.Query("mode"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	category := c.Query("category")
	theme := c.Query("theme")
	page, pageSize := httpx.PageParams(c)
	items, total, err := h.service.RecommendChannelsByMode(mode, category, theme, page, pageSize)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, page, pageSize, total)
}

func (h *Handler) getRecommendationThemes(c *gin.Context) {
	category := c.Query("category")
	themes := h.service.ListRecommendationThemes(category)
	httpx.List(c, themes, 1, len(themes), int64(len(themes)))
}

func (h *Handler) markRead(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req struct {
		FeedItemIDs []uuid.UUID `json:"feed_item_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	if err := h.service.MarkRead(user, req.FeedItemIDs); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) markUnread(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req struct {
		FeedItemIDs []uuid.UUID `json:"feed_item_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	if err := h.service.MarkUnread(user, req.FeedItemIDs); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) markAllRead(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if err := h.service.MarkAllRead(user); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) markAllUnread(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if err := h.service.MarkAllUnread(user); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) toggleStar(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req struct {
		FeedItemID uuid.UUID `json:"feed_item_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	starred, err := h.service.ToggleStar(user, req.FeedItemID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"starred": starred})
}

// listReadingList godoc
// @Summary 获取统一稍后阅读列表
// @Tags feed
// @Produce json
// @Param page query int false "页码"
// @Param page_size query int false "每页数量"
// @Success 200 {array} model.ReadingListItem
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/reading-list [get]
func (h *Handler) listReadingList(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	query, err := queryFromContext(c)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	items, total, err := h.service.ListReadingList(user, query)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, normalizedPageFromQuery(c), normalizedPageSizeFromQuery(c), total)
}

func (h *Handler) recordReadEvent(c *gin.Context) {
	var req struct {
		SourceType string `json:"source_type"`
		SourceID   string `json:"source_id"`
		EventType  string `json:"event_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	if err := h.service.RecordSourceReadEvent(strings.TrimSpace(req.SourceType), strings.TrimSpace(req.SourceID), strings.TrimSpace(req.EventType)); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

// toggleReadingList godoc
// @Summary 切换稍后阅读状态
// @Tags feed
// @Accept json
// @Produce json
// @Param input body ReadingListInput true "稍后阅读目标"
// @Success 200 {object} map[string]bool
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/reading-list [post]
func (h *Handler) toggleReadingList(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req ReadingListInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	saved, err := h.service.ToggleReadingList(user, strings.TrimSpace(req.TargetType), req.TargetID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"saved": saved})
}

// removeReadingListItem godoc
// @Summary 从待读列表移除条目
// @Description 删除当前用户稍后阅读列表中的指定 RSS 条目或站内文章。
// @Tags feed
// @Produce json
// @Param target_type path string true "目标类型" Enums(feed_item,post)
// @Param id path string true "目标 UUID"
// @Success 200 {object} handlers.RemoveStatusResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/reading-list/{target_type}/{id} [delete]
func (h *Handler) removeReadingListItem(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "feed item id must be a valid uuid"))
		return
	}
	if err := h.service.RemoveReadingListItem(user, c.Param("target_type"), targetID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"removed": true})
}

func queryFromContext(c *gin.Context) (FeedQuery, error) {
	contentType := strings.TrimSpace(strings.ToLower(c.Query("content_type")))
	if contentType != "" {
		if _, err := studioapi.ParseModule(contentType); err != nil {
			return FeedQuery{}, err
		}
	}
	query := FeedQuery{
		Page:           normalizedPageFromQuery(c),
		PageSize:       normalizedPageSizeFromQuery(c),
		ContentType:    contentType,
		SourceType:     c.Query("source_type"),
		HideDuplicates: c.Query("hide_duplicates") == "true",
		Sort:           c.DefaultQuery("sort", "recent"),
		Search:         strings.TrimSpace(c.Query("q")),
	}
	if raw := c.Query("source_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return FeedQuery{}, apperr.BadRequest("validation.invalid_request", "source_id must be a valid uuid")
		}
		query.SourceID = id
	}
	if raw := c.Query("group_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return FeedQuery{}, apperr.BadRequest("validation.invalid_request", "group_id must be a valid uuid")
		}
		query.GroupID = id
	}
	if raw := c.Query("is_read"); raw != "" {
		value := raw == "true"
		query.IsRead = &value
	} else if c.Query("unread_only") == "true" {
		value := false
		query.IsRead = &value
	}
	return query, nil
}

func normalizedPageFromQuery(c *gin.Context) int {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		return 1
	}
	return page
}

func normalizedPageSizeFromQuery(c *gin.Context) int {
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", c.DefaultQuery("limit", "20")))
	if pageSize < 1 {
		return 20
	}
	if pageSize > 100 {
		return 100
	}
	return pageSize
}
