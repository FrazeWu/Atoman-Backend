package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/collab"
	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/service"
)

type notificationHandler struct {
	svc     *service.NotificationService
	userHub *collab.UserHub
}

func SetupNotificationRoutes(r *gin.Engine, db *gorm.DB, userHub *collab.UserHub) {
	h := &notificationHandler{
		svc:     service.NewNotificationService(db),
		userHub: userHub,
	}

	auth := r.Group("/api/notifications")
	auth.Use(middleware.AuthMiddleware())
	{
		auth.GET("", h.list)
		auth.GET("/unread-count", h.unreadCount)
		auth.PUT("/:id/read", h.markRead)
		auth.PUT("/read-all", h.markAllRead)
	}
}

// list godoc
// @Summary 获取通知列表
// @Description 返回当前用户的通知列表，可按类型筛选。
// @Tags notifications
// @Produce json
// @Param type query string false "通知类型"
// @Param page query int false "页码" default(1)
// @Success 200 {object} NotificationListResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/notifications [get]
func (h *notificationHandler) list(c *gin.Context) {
	userID, ok := c.Get("user_id")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	recipientID := userID.(uuid.UUID)

	notifType := c.Query("type")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}

	notifications, total, err := h.svc.List(recipientID, notifType, page, 20)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch notifications"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":  notifications,
		"total": total,
		"page":  page,
	})
}

// unreadCount godoc
// @Summary 获取未读通知数
// @Description 返回当前用户的未读通知数量。
// @Tags notifications
// @Produce json
// @Success 200 {object} CountResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/notifications/unread-count [get]
func (h *notificationHandler) unreadCount(c *gin.Context) {
	userID, ok := c.Get("user_id")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	recipientID := userID.(uuid.UUID)

	count, err := h.svc.UnreadCount(recipientID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count notifications"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"count": count})
}

// markRead godoc
// @Summary 标记单条通知已读
// @Description 将指定通知标记为已读。
// @Tags notifications
// @Produce json
// @Param id path string true "通知 UUID"
// @Success 200 {object} BoolStatusResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/notifications/{id}/read [put]
func (h *notificationHandler) markRead(c *gin.Context) {
	userID, ok := c.Get("user_id")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	recipientID := userID.(uuid.UUID)

	notifID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	if err := h.svc.MarkRead(notifID, recipientID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark read"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// markAllRead godoc
// @Summary 标记所有通知已读
// @Description 将当前用户的通知全部标记为已读，可按类型过滤。
// @Tags notifications
// @Produce json
// @Param type query string false "通知类型"
// @Success 200 {object} BoolStatusResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/notifications/read-all [put]
func (h *notificationHandler) markAllRead(c *gin.Context) {
	userID, ok := c.Get("user_id")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	recipientID := userID.(uuid.UUID)

	if err := h.svc.MarkAllRead(recipientID, c.Query("type")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark all read"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func WsPushNotif(userHub *collab.UserHub) func(uuid.UUID, *model.Notification) {
	return func(recipientID uuid.UUID, notif *model.Notification) {
		if userHub == nil || notif == nil {
			return
		}
		userHub.Push(recipientID, "notification", notif)
	}
}
