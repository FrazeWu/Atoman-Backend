package dm

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"atoman/internal/middleware"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Handler struct{ service *Service }

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	dm := group.Group("/dm")
	dm.Use(middleware.StableAuthMiddleware())
	dm.GET("/mailboxes", h.listMailboxes)
	dm.GET("/mailboxes/:type/:id/conversations", h.listConversations)
	dm.GET("/targets/:type/:id/conversation", h.getTargetConversation)
	dm.POST("/targets/:type/:id/messages", h.sendToTarget)
	dm.POST("/conversations/:id/messages", h.sendInConversation)
	dm.GET("/conversations/:id/messages", h.listMessages)
	dm.PUT("/conversations/:id/read", h.markRead)
	dm.PUT("/conversations/:id/block", h.block)
	dm.DELETE("/conversations/:id/block", h.unblock)
	dm.POST("/images", h.uploadImage)
	dm.GET("/images/:id/content", h.openImage)
	dm.POST("/messages/:id/reports", h.report)
	dm.GET("/settings", h.getSettings)
	dm.PUT("/settings", h.updateSettings)
	dm.GET("/channels/:id/settings", h.getChannelSettings)
	dm.PUT("/channels/:id/settings", h.updateChannelSettings)
	admin := group.Group("/admin/dm")
	admin.Use(middleware.StableAuthMiddleware())
	admin.GET("/reports", h.listReports)
	admin.PUT("/reports/:id", h.reviewReport)
}

func (h *Handler) actor(c *gin.Context) (authctx.CurrentUser, bool) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Authorization required"))
	}
	return user, ok
}
func parseTarget(c *gin.Context) (TargetRef, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a UUID"))
		return TargetRef{}, false
	}
	return TargetRef{Type: c.Param("type"), ID: id}, true
}
func parseID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a UUID"))
		return uuid.Nil, false
	}
	return id, true
}
func limit(c *gin.Context) int { value, _ := strconv.Atoi(c.Query("limit")); return value }
func (h *Handler) fail(c *gin.Context, err error) {
	for domain, mapped := range map[error]struct {
		status int
		code   string
	}{ErrTargetNotFound: {404, "dm.target_not_found"}, ErrSelfTarget: {400, "dm.self_target"}, ErrPermissionDenied: {403, "dm.permission_denied"}, ErrWaitingReply: {403, "dm.waiting_reply"}, ErrBlocked: {403, "dm.blocked"}, ErrConversationForbidden: {403, "dm.conversation_forbidden"}, ErrImageInvalid: {400, "dm.image_invalid"}, ErrRateLimited: {429, "dm.rate_limited"}, ErrMessageNotFound: {404, "dm.message_not_found"}, ErrAlreadyReported: {409, "dm.already_reported"}} {
		if errors.Is(err, domain) {
			c.JSON(mapped.status, gin.H{"error": gin.H{"code": mapped.code, "message": mapped.code}})
			return
		}
	}
	httpx.Error(c, err)
}

// @Summary DM mailboxes
// @Tags dm
// @Security BearerAuth
// @Success 200 {object} MailboxesResponse
// @Router /api/v1/dm/mailboxes [get]
func (h *Handler) listMailboxes(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	data, err := h.service.ListMailboxes(c, actor)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, 200, data)
}

// @Summary DM mailbox conversations
// @Tags dm
// @Security BearerAuth
// @Param type path string true "party type"
// @Param id path string true "mailbox ID"
// @Param cursor query string false "cursor"
// @Param limit query int false "limit"
// @Success 200 {object} ConversationsResponse
// @Router /api/v1/dm/mailboxes/{type}/{id}/conversations [get]
func (h *Handler) listConversations(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	mailbox, ok := parseTarget(c)
	if !ok {
		return
	}
	data, err := h.service.ListConversations(c, actor, mailbox, c.Query("cursor"), limit(c))
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, 200, data)
}

// @Summary Find DM target conversation
// @Tags dm
// @Security BearerAuth
// @Param type path string true "target type"
// @Param id path string true "target ID"
// @Success 200 {object} ConversationResponse
// @Success 204
// @Router /api/v1/dm/targets/{type}/{id}/conversation [get]
func (h *Handler) getTargetConversation(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	target, ok := parseTarget(c)
	if !ok {
		return
	}
	data, err := h.service.GetTargetConversation(c, actor.ID, target)
	if errors.Is(err, ErrTargetNotFound) || errors.Is(err, ErrSelfTarget) {
		h.fail(c, err)
		return
	}
	if err != nil {
		if errors.Is(err, ErrConversationForbidden) {
			c.Status(http.StatusNoContent)
			return
		}
		if strings.Contains(err.Error(), "record not found") {
			c.Status(http.StatusNoContent)
			return
		}
		h.fail(c, err)
		return
	}
	httpx.OK(c, 200, data)
}
func (h *Handler) input(c *gin.Context) (SendInput, bool) {
	var input SendInput
	if err := c.ShouldBindJSON(&input); err != nil || input.ClientMessageID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "client_message_id is required"))
		return SendInput{}, false
	}
	return input, true
}

// @Summary Send DM to target
// @Tags dm
// @Security BearerAuth
// @Param type path string true "target type"
// @Param id path string true "target ID"
// @Param input body SendInput true "message"
// @Success 201 {object} MessageResponse
// @Router /api/v1/dm/targets/{type}/{id}/messages [post]
func (h *Handler) sendToTarget(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	target, ok := parseTarget(c)
	if !ok {
		return
	}
	input, ok := h.input(c)
	if !ok {
		return
	}
	data, err := h.service.SendToTarget(c, actor.ID, target, input)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, data)
}

// @Summary Send DM in conversation
// @Tags dm
// @Security BearerAuth
// @Param id path string true "conversation ID"
// @Param input body SendInput true "message"
// @Success 201 {object} MessageResponse
// @Router /api/v1/dm/conversations/{id}/messages [post]
func (h *Handler) sendInConversation(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	input, ok := h.input(c)
	if !ok {
		return
	}
	data, err := h.service.SendInConversation(c, actor.ID, id, input)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, data)
}

// @Summary List DM messages
// @Tags dm
// @Security BearerAuth
// @Param id path string true "conversation ID"
// @Param before query string false "cursor"
// @Param limit query int false "limit"
// @Success 200 {object} MessagesResponse
// @Router /api/v1/dm/conversations/{id}/messages [get]
func (h *Handler) listMessages(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	data, err := h.service.ListMessages(c, actor, id, c.Query("before"), limit(c))
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, 200, data)
}

// @Summary Mark DM conversation read
// @Tags dm
// @Security BearerAuth
// @Param id path string true "conversation ID"
// @Success 200 {object} ReadResponse
// @Router /api/v1/dm/conversations/{id}/read [put]
func (h *Handler) markRead(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	data, err := h.service.MarkRead(c, actor, id)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, 200, data)
}

// @Summary Block DM conversation
// @Tags dm
// @Security BearerAuth
// @Param id path string true "conversation ID"
// @Success 200 {object} ConversationResponse
// @Router /api/v1/dm/conversations/{id}/block [put]
func (h *Handler) block(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	data, err := h.service.BlockConversation(c, actor, id)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, 200, data)
}

// @Summary Unblock DM conversation
// @Tags dm
// @Security BearerAuth
// @Param id path string true "conversation ID"
// @Success 200 {object} ConversationResponse
// @Router /api/v1/dm/conversations/{id}/block [delete]
func (h *Handler) unblock(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	data, err := h.service.UnblockConversation(c, actor, id)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, 200, data)
}

// @Summary Upload private DM image
// @Tags dm
// @Security BearerAuth
// @Accept mpfd
// @Param image formData file true "image"
// @Success 201 {object} ImageResponse
// @Router /api/v1/dm/images [post]
func (h *Handler) uploadImage(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	file, err := c.FormFile("image")
	if err != nil {
		h.fail(c, ErrImageInvalid)
		return
	}
	opened, err := file.Open()
	if err != nil {
		h.fail(c, ErrImageInvalid)
		return
	}
	defer opened.Close()
	data, err := h.service.UploadImage(c, actor, opened, file.Header.Get("Content-Type"), file.Size)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, data)
}

// @Summary Read private DM image
// @Tags dm
// @Security BearerAuth
// @Param id path string true "image ID"
// @Success 200 {file} file
// @Router /api/v1/dm/images/{id}/content [get]
func (h *Handler) openImage(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	body, contentType, err := h.service.OpenImage(c, actor, id)
	if err != nil {
		h.fail(c, err)
		return
	}
	defer body.Close()
	c.DataFromReader(200, -1, contentType, body, nil)
}

// @Summary Report DM message
// @Tags dm
// @Security BearerAuth
// @Param id path string true "message ID"
// @Param input body ReportInput true "report"
// @Success 201 {object} ReportReceiptResponse
// @Router /api/v1/dm/messages/{id}/reports [post]
func (h *Handler) report(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input ReportInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "invalid report"))
		return
	}
	data, err := h.service.ReportMessage(c, actor, id, input)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, data)
}

// @Summary List DM reports
// @Tags dm-admin
// @Security BearerAuth
// @Param cursor query string false "cursor"
// @Param limit query int false "limit"
// @Success 200 {object} ReportsResponse
// @Router /api/v1/admin/dm/reports [get]
func (h *Handler) listReports(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	data, err := h.service.ListReports(c, actor, c.Query("cursor"), limit(c))
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, 200, data)
}

// @Summary Review DM report
// @Tags dm-admin
// @Security BearerAuth
// @Param id path string true "report ID"
// @Param input body ReviewReportInput true "review"
// @Success 200 {object} ReportResponse
// @Router /api/v1/admin/dm/reports/{id} [put]
func (h *Handler) reviewReport(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input ReviewReportInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "invalid report status"))
		return
	}
	data, err := h.service.ReviewReport(c, actor, id, input)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, 200, data)
}

// @Summary Get DM permission
// @Tags dm
// @Security BearerAuth
// @Success 200 {object} PermissionResponse
// @Router /api/v1/dm/settings [get]
func (h *Handler) getSettings(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	data, err := h.service.GetUserSettings(c, actor)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, 200, data)
}

// @Summary Update DM permission
// @Tags dm
// @Security BearerAuth
// @Param input body PermissionDTO true "permission"
// @Success 200 {object} PermissionResponse
// @Router /api/v1/dm/settings [put]
func (h *Handler) updateSettings(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	var input PermissionDTO
	if err := c.ShouldBindJSON(&input); err != nil {
		h.fail(c, ErrPermissionDenied)
		return
	}
	data, err := h.service.UpdateUserSettings(c, actor, input.Permission)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, 200, data)
}

// @Summary Get channel DM permission
// @Tags dm
// @Security BearerAuth
// @Param id path string true "channel ID"
// @Success 200 {object} PermissionResponse
// @Router /api/v1/dm/channels/{id}/settings [get]
func (h *Handler) getChannelSettings(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	data, err := h.service.GetChannelSettings(c, actor, id)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, 200, data)
}

// @Summary Update channel DM permission
// @Tags dm
// @Security BearerAuth
// @Param id path string true "channel ID"
// @Param input body PermissionDTO true "permission"
// @Success 200 {object} PermissionResponse
// @Router /api/v1/dm/channels/{id}/settings [put]
func (h *Handler) updateChannelSettings(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input PermissionDTO
	if err := c.ShouldBindJSON(&input); err != nil {
		h.fail(c, ErrPermissionDenied)
		return
	}
	data, err := h.service.UpdateChannelSettings(c, actor, id, input.Permission)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, 200, data)
}
