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
	group.GET("/notifications/unread-counts", h.getUnreadCounts)
	group.PUT("/notifications/:id/read", h.markRead)
	group.PUT("/notifications/read-all", h.markAllRead)
	group.GET("/notifications/preferences", h.listPreferences)
	group.PUT("/notifications/preferences", h.savePreferences)
	group.POST("/notifications/mutes", h.createMute)
}

func RegisterAdminRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.POST("/admin/announcements", h.publishAnnouncement)
}

// publishAnnouncement godoc
// @Summary 发布站点公告
// @Description 仅管理员可发布，公告会作为系统通知投递给所有活跃用户。
// @Tags notifications
// @Accept json
// @Produce json
// @Param payload body PublishAnnouncementInput true "公告内容"
// @Success 201 {object} PublishAnnouncementResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/announcements [post]
func (h *Handler) publishAnnouncement(c *gin.Context) {
	var input PublishAnnouncementInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Announcement content is invalid"))
		return
	}
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	delivered, err := h.service.PublishAnnouncement(user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, PublishAnnouncementResponse{Delivered: delivered})
}

// listPreferences godoc
// @Summary 获取通知偏好
// @Tags notifications
// @Produce json
// @Success 200 {array} model.NotificationPreference
// @Failure 401 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/notifications/preferences [get]
func (h *Handler) listPreferences(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	items, err := h.service.ListPreferences(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, items)
}

// savePreferences godoc
// @Summary 保存通知偏好
// @Tags notifications
// @Accept json
// @Produce json
// @Param payload body SavePreferencesInput true "通知偏好"
// @Success 200 {array} model.NotificationPreference
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/notifications/preferences [put]
func (h *Handler) savePreferences(c *gin.Context) {
	var input SavePreferencesInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Notification preferences are invalid"))
		return
	}
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	items, err := h.service.SavePreferences(user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, items)
}

// createMute godoc
// @Summary 静音通知来源
// @Tags notifications
// @Accept json
// @Produce json
// @Param payload body CreateMuteInput true "通知来源"
// @Success 201 {object} model.NotificationMute
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/notifications/mutes [post]
func (h *Handler) createMute(c *gin.Context) {
	var input CreateMuteInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Notification source is invalid"))
		return
	}
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	mute, err := h.service.CreateMute(user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, mute)
}

// getUnreadCounts godoc
// @Summary 获取分类未读数
// @Description 返回通知分类和私信的未读数量及总数。
// @Tags notifications
// @Produce json
// @Success 200 {object} UnreadCountsResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/notifications/unread-counts [get]
func (h *Handler) getUnreadCounts(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	counts, err := h.service.GetUnreadCounts(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, counts)
}

// listNotifications godoc
// @Summary 获取通知列表
// @Description type 为精确类型筛选且优先于 category；category 可为 like、interaction、mention、reply、collaboration 或 system，system 包含未归入已知分类的开放类型。
// @Tags notifications
// @Produce json
// @Param page query int false "页码"
// @Param page_size query int false "每页数量"
// @Param type query string false "精确通知类型，优先于 category"
// @Param category query string false "通知分类"
// @Success 200 {array} NotificationDTO
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/notifications [get]
func (h *Handler) listNotifications(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	query := ListQuery{Page: normalizedPageFromQuery(c), PageSize: normalizedPageSizeFromQuery(c), Type: strings.TrimSpace(c.Query("type")), Category: strings.TrimSpace(c.Query("category"))}
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

// markAllRead godoc
// @Summary 全部标记为已读
// @Description type 为精确类型筛选且优先于 category；category 的 system 匹配未归入已知分类的开放类型。
// @Tags notifications
// @Produce json
// @Param type query string false "精确通知类型，优先于 category"
// @Param category query string false "通知分类"
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/notifications/read-all [put]
func (h *Handler) markAllRead(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	query := ListQuery{Type: strings.TrimSpace(c.Query("type")), Category: strings.TrimSpace(c.Query("category"))}
	if err := h.service.MarkAllRead(user, query); err != nil {
		httpx.Error(c, err)
		return
	}
	unreadTotal, err := h.service.GetUnreadCount(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true, "unread_total": unreadTotal})
}

func normalizedPageFromQuery(c *gin.Context) int {
	page, _ := httpx.PageParams(c)
	return page
}

func normalizedPageSizeFromQuery(c *gin.Context) int {
	_, pageSize := httpx.PageParams(c)
	return pageSize
}
