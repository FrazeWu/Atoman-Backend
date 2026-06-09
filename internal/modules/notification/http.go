package notification

import (
	"net/http"
	"strings"

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
	group.GET("/notifications", h.listNotifications)
	group.GET("/notifications/unread-count", h.getUnreadCount)
	group.PUT("/notifications/:id/read", h.markRead)
	group.PUT("/notifications/read-all", h.markAllRead)
}

func (h *Handler) listNotifications(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	query := ListQuery{Page: normalizedPageFromQuery(c), PageSize: normalizedPageSizeFromQuery(c), Type: strings.TrimSpace(c.Query("type"))}
	items, total, err := h.service.ListNotifications(user, query)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, query.Page, query.PageSize, total)
}

func (h *Handler) getUnreadCount(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	count, err := h.service.GetUnreadCount(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"count": count})
}

func (h *Handler) markRead(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "notification id must be a valid UUID"))
		return
	}
	if err := h.service.MarkRead(user, id); err != nil {
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
	if err := h.service.MarkAllRead(user, c.Query("type")); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

func normalizedPageFromQuery(c *gin.Context) int {
	page, _ := httpx.PageParams(c)
	return page
}

func normalizedPageSizeFromQuery(c *gin.Context) int {
	_, pageSize := httpx.PageParams(c)
	return pageSize
}
