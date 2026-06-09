package feed

import (
	"net/http"
	"strconv"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Handler struct {
	service *Service
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.GET("/subscriptions", h.getSubscribedFeed)
	group.GET("/timeline", h.getSubscribedFeed)
	group.GET("/explore", h.getExploreFeed)
	group.POST("/timeline/mark-read", h.markRead)
	group.POST("/timeline/mark-all-read", h.markAllRead)
	group.POST("/timeline/mark-all-unread", h.markAllUnread)
	group.POST("/timeline/star", h.toggleStar)
	group.POST("/reading-list", h.toggleReadingList)
}

func (h *Handler) getSubscribedFeed(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	items, total, err := h.service.GetSubscribedFeed(user, queryFromContext(c))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, normalizedPageFromQuery(c), normalizedPageSizeFromQuery(c), total)
}

func (h *Handler) getExploreFeed(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	items, total, err := h.service.GetExploreFeed(user, queryFromContext(c))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, normalizedPageFromQuery(c), normalizedPageSizeFromQuery(c), total)
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

func (h *Handler) toggleReadingList(c *gin.Context) {
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
	saved, err := h.service.ToggleReadingList(user, req.FeedItemID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"saved": saved})
}

func queryFromContext(c *gin.Context) FeedQuery {
	query := FeedQuery{
		Page:           normalizedPageFromQuery(c),
		PageSize:       normalizedPageSizeFromQuery(c),
		SourceType:     c.Query("source_type"),
		HideDuplicates: c.Query("hide_duplicates") == "true",
		Sort:           c.DefaultQuery("sort", "recent"),
	}
	if raw := c.Query("source_id"); raw != "" {
		if id, err := uuid.Parse(raw); err == nil {
			query.SourceID = id
		}
	}
	if raw := c.Query("group_id"); raw != "" {
		if id, err := uuid.Parse(raw); err == nil {
			query.GroupID = id
		}
	}
	if raw := c.Query("is_read"); raw != "" {
		value := raw == "true"
		query.IsRead = &value
	}
	return query
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
