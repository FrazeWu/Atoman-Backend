package lifecycle

import (
	"net/http"
	"strconv"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Handler struct{ service *Service }

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.POST("/events", h.recordEvent)
	group.PUT("/progress", h.saveProgress)
	group.GET("/progress/:module/:id", h.getProgress)
	group.GET("/continue", h.listContinue)
	group.GET("/notification-preferences", h.listNotificationPreferences)
	group.PUT("/notification-preferences", h.saveNotificationPreference)
	group.GET("/:module/:id/schedule", h.getSchedule)
	group.POST("/:module/:id/schedule", h.scheduleContent)
	group.POST("/:module/:id/schedule/retry", h.retrySchedule)
	group.DELETE("/:module/:id/schedule", h.cancelSchedule)
}

// recordEvent godoc
// @Summary POST /content/events
// @Tags content
// @Success 200 {object} map[string]interface{}
// @Failure 401 "Unauthorized"
// @Router /api/v1/content/events [post]
func (h *Handler) recordEvent(c *gin.Context) {
	var input EventInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	user, _ := authctx.Current(c)
	if err := h.service.RecordEvent(user, input); err != nil {
		// saveProgress godoc
		// @Summary PUT /content/progress
		// @Tags content
		// @Success 200 {object} map[string]interface{}
		// @Failure 401 "Unauthorized"
		// @Router /api/v1/content/progress [put]
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, gin.H{"recorded": true})
}

func (h *Handler) saveProgress(c *gin.Context) {
	var input ProgressInput
	// getProgress godoc
	// @Summary GET /content/progress/{module}/{id}
	// @Tags content
	// @Success 200 {object} map[string]interface{}
	// @Failure 401 "Unauthorized"
	// @Router /api/v1/content/progress/{module}/{id} [get]
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	user, _ := authctx.Current(c)
	progress, err := h.service.SaveProgress(user, input)
	if err != nil {
		httpx.Error(c, err)
		return
		// listContinue godoc
		// @Summary GET /content/continue
		// @Tags content
		// @Success 200 {object} map[string]interface{}
		// @Failure 401 "Unauthorized"
		// @Router /api/v1/content/continue [get]
	}
	httpx.OK(c, http.StatusOK, progress)
}

func (h *Handler) getProgress(c *gin.Context) {
	// listNotificationPreferences godoc
	// @Summary GET /content/notification-preferences
	// @Tags content
	// @Success 200 {object} map[string]interface{}
	// @Failure 401 "Unauthorized"
	// @Router /api/v1/content/notification-preferences [get]
	contentID, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	// saveNotificationPreference godoc
	// @Summary PUT /content/notification-preferences
	// @Tags content
	// @Success 200 {object} map[string]interface{}
	// @Failure 401 "Unauthorized"
	// @Router /api/v1/content/notification-preferences [put]
	user, _ := authctx.Current(c)
	progress, err := h.service.GetProgress(user, c.Param("module"), contentID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, progress)
}

func (h *Handler) listContinue(c *gin.Context) {
	user, _ := authctx.Current(c)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "12"))
	items, err := h.service.ListContinue(user, c.Query("module"), limit)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, items)
}

func (h *Handler) listNotificationPreferences(c *gin.Context) {
	// getSchedule godoc
	// @Summary GET /content/{module}/{id}/schedule
	// @Tags content
	// @Success 200 {object} map[string]interface{}
	// @Failure 401 "Unauthorized"
	// @Router /api/v1/content/{module}/{id}/schedule [get]
	user, _ := authctx.Current(c)
	items, err := h.service.ListNotificationPreferences(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, items)
}

func (h *Handler) saveNotificationPreference(c *gin.Context) {
	var input NotificationPreferenceInput
	if !httpx.BindRequiredJSON(c, &input) {
		// scheduleContent godoc
		// @Summary POST /content/{module}/{id}/schedule
		// @Tags content
		// @Success 200 {object} map[string]interface{}
		// @Failure 401 "Unauthorized"
		// @Router /api/v1/content/{module}/{id}/schedule [post]
		return
	}
	user, _ := authctx.Current(c)
	item, err := h.service.SaveNotificationPreference(user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, item)
}

// cancelSchedule godoc
// @Summary DELETE /content/{module}/{id}/schedule
// @Tags content
// @Success 200 {object} map[string]interface{}
// @Failure 401 "Unauthorized"
// @Router /api/v1/content/{module}/{id}/schedule [delete]
// getSchedule godoc
// @Summary 查询定时发布状态
// @Tags lifecycle
// @Produce json
// @Param module path string true "内容模块" Enums(blog)
// @Param id path string true "内容 UUID"
// @Success 200 {object} BlogScheduleStatus
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/lifecycle/{module}/{id}/schedule [get]
func (h *Handler) getSchedule(c *gin.Context) {
	contentID, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	if c.Param("module") != "blog" {
		httpx.Error(c, apperr.BadRequest("lifecycle.invalid_module", "schedule status is available for blog content"))
		return
	}
	user, _ := authctx.Current(c)
	status, err := h.service.GetBlogSchedule(user, contentID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, status)
}

func (h *Handler) scheduleContent(c *gin.Context) {
	contentID, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	var input ScheduleInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	input.Module = c.Param("module")
	input.ContentID = contentID
	user, _ := authctx.Current(c)
	result, err := h.service.ScheduleContent(user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

func (h *Handler) cancelSchedule(c *gin.Context) {
	contentID, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	user, _ := authctx.Current(c)
	if err := h.service.CancelSchedule(user, c.Param("module"), contentID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"cancelled": true})
}

// retrySchedule godoc
// @Summary 重试失败的定时发布
// @Tags lifecycle
// @Produce json
// @Param module path string true "内容模块" Enums(blog)
// @Param id path string true "内容 UUID"
// @Success 200 {object} BlogScheduleStatus
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/lifecycle/{module}/{id}/schedule/retry [post]
func (h *Handler) retrySchedule(c *gin.Context) {
	contentID, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	if c.Param("module") != "blog" {
		httpx.Error(c, apperr.BadRequest("lifecycle.invalid_module", "schedule retry is available for blog content"))
		return
	}
	user, _ := authctx.Current(c)
	status, err := h.service.RetryBlogSchedule(user, contentID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, status)
}

func pathUUID(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_id", name+" must be a UUID"))
		return uuid.Nil, false
	}
	return id, true
}
